package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEncodeDecode28BitValue(t *testing.T) {
	testValues := []int{
		0,
		1,
		-1,
		64,
		-64,
		1023,
		-1023,
		12000,
		-12000,
		0x03FFFFFF,  // max positive 27-bit magnitude
		-0x04000000, // min negative 28-bit
	}

	for _, v := range testValues {
		dd1, dd2, dd3, dd4 := Encode28BitValue(v)
		decoded := Decode28BitValue(dd1, dd2, dd3, dd4)
		if decoded != v {
			t.Errorf("Encode/Decode mismatch for value %d: got %d (bytes: %02X %02X %02X %02X)",
				v, decoded, dd1, dd2, dd3, dd4)
		}
	}
}

func TestParse01V96SysEx(t *testing.T) {
	// 1. Valid 14-byte Fader SysEx: Element 0x1C (Fader), Param 0x00, Ch 0 (CH 1), Value 820
	// 820 = 0x0334 -> dd1=0, dd2=0, dd3=6, dd4=0x34
	msgFader := Build01V96SysEx(0x1C, 0x00, 0x00, 820)
	elem, param, ch, val, ok := Parse01V96SysEx(msgFader)
	if !ok {
		t.Fatalf("Parse01V96SysEx failed for valid fader sysex: % X", msgFader)
	}
	if elem != 0x1C || param != 0x00 || ch != 0x00 || val != 820 {
		t.Errorf("Decoded mismatch: elem=%02X param=%02X ch=%02X val=%d", elem, param, ch, val)
	}

	// 2. Valid SysEx with negative value (e.g. Pan L32 = -32, or EQ gain -120)
	dd1, dd2, dd3, dd4 := Encode28BitValue(-120)
	msgNegative := []byte{0xF0, 0x43, 0x10, 0x3E, 0x0D, 0x01, 0x20, 0x03, 0x05, dd1, dd2, dd3, dd4, 0xF7}
	elem, param, ch, val, ok = Parse01V96SysEx(msgNegative)
	if !ok {
		t.Fatalf("Parse01V96SysEx failed for negative value: % X", msgNegative)
	}
	if elem != 0x20 || param != 0x03 || ch != 0x05 || val != -120 {
		t.Errorf("Decoded negative mismatch: elem=%02X param=%02X ch=%02X val=%d", elem, param, ch, val)
	}

	// 3. Valid SysEx with Universal Digital Mixer Model ID (0x7F) and Channel 1 (0x10)
	msgUniversal := []byte{0xF0, 0x43, 0x10, 0x3E, 0x7F, 0x01, 0x1A, 0x00, 0x02, 0, 0, 0, 1, 0xF7}
	elem, param, ch, val, ok = Parse01V96SysEx(msgUniversal)
	if !ok {
		t.Fatalf("Parse01V96SysEx failed for Universal ID 0x7F: % X", msgUniversal)
	}
	if elem != 0x1A || param != 0x00 || ch != 0x02 || val != 1 {
		t.Errorf("Decoded universal mismatch: elem=%02X param=%02X ch=%02X val=%d", elem, param, ch, val)
	}

	// 4. Invalid Messages
	invalidCases := [][]byte{
		{},                                                         // empty
		{0xF0, 0x43, 0x10, 0x3E, 0x0D, 0x01, 0x1C, 0xF7},           // too short
		{0xF0, 0x44, 0x10, 0x3E, 0x0D, 0x01, 0x1C, 0, 0, 0, 0, 0, 0, 0xF7}, // wrong manufacturer (0x44 != 0x43)
		{0xF0, 0x43, 0x20, 0x3E, 0x0D, 0x01, 0x1C, 0, 0, 0, 0, 0, 0, 0xF7}, // wrong substatus (0x20 != 0x1n)
		{0xF0, 0x43, 0x10, 0x3E, 0x0D, 0x02, 0x1C, 0, 0, 0, 0, 0, 0, 0xF7}, // wrong data type (0x02 != 0x01)
		{0xF0, 0x43, 0x10, 0x3E, 0x0D, 0x01, 0x1C, 0, 0, 0, 0, 0, 0, 0xF8}, // wrong EOX (0xF8 != 0xF7)
	}

	for i, bad := range invalidCases {
		if _, _, _, _, ok := Parse01V96SysEx(bad); ok {
			t.Errorf("Invalid case %d should have failed: % X", i, bad)
		}
	}
}

func TestBuild01V96ParamRequest(t *testing.T) {
	req := Build01V96ParamRequest(0x1C, 0x00, 0x03)
	expected := []byte{0xF0, 0x43, 0x30, 0x3E, 0x7F, 0x01, 0x1C, 0x00, 0x03, 0xF7}

	if len(req) != len(expected) {
		t.Fatalf("Param request length mismatch: got %d, expected %d", len(req), len(expected))
	}
	for i := range expected {
		if req[i] != expected[i] {
			t.Errorf("Byte %d mismatch: got 0x%02X, expected 0x%02X", i, req[i], expected[i])
		}
	}
}

func TestEchoFilter(t *testing.T) {
	elem, param, ch, val := byte(0x1C), byte(0x00), byte(0x05), 750

	// Before marking, should not be an echo
	if isSentEcho(elem, param, ch, val) {
		t.Errorf("Expected isSentEcho to be false initially")
	}

	// Mark echo
	markSentEcho(elem, param, ch, val)

	// Immediately check: should be recognized as echo
	if !isSentEcho(elem, param, ch, val) {
		t.Errorf("Expected isSentEcho to be true after marking")
	}

	// Second check: already consumed, should be false
	if isSentEcho(elem, param, ch, val) {
		t.Errorf("Expected isSentEcho to be false after being consumed once")
	}
}

func TestGenerateSyncRequestList(t *testing.T) {
	critical, detail := generateSyncRequestList()

	if len(critical) == 0 {
		t.Fatalf("Expected critical requests, got 0")
	}
	if len(detail) == 0 {
		t.Fatalf("Expected detail requests, got 0")
	}

	// Total should be around 1400 requests
	total := len(critical) + len(detail)
	if total < 1000 {
		t.Errorf("Expected >1000 requests for full 01V96 state query, got %d", total)
	}
}

func TestHandleAPISyncMixer(t *testing.T) {
	// 1. Unauthenticated request -> 401
	reqUnauth := httptest.NewRequest(http.MethodPost, "/api/state/sync-mixer", nil)
	rrUnauth := httptest.NewRecorder()
	handleAPISyncMixer(rrUnauth, reqUnauth)

	if rrUnauth.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401 Unauthorized for unauthenticated request, got %d", rrUnauth.Code)
	}

	// 2. Authenticated request (simulation mode) -> 200
	token := "test-sync-token"
	sessionsLock.Lock()
	sessions[token] = &Session{
		Token: token,
		User: User{
			Username: "admin",
			Name:     "Admin",
			Role:     "ADMIN",
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	sessionsLock.Unlock()

	reqAuth := httptest.NewRequest(http.MethodPost, "/api/state/sync-mixer", nil)
	reqAuth.AddCookie(&http.Cookie{
		Name:  "remote_session",
		Value: token,
	})
	rrAuth := httptest.NewRecorder()
	handleAPISyncMixer(rrAuth, reqAuth)

	if rrAuth.Code != http.StatusOK {
		t.Errorf("Expected status 200 OK for authenticated sync request, got %d", rrAuth.Code)
	}

	var res map[string]interface{}
	if err := json.Unmarshal(rrAuth.Body.Bytes(), &res); err != nil {
		t.Fatalf("Failed to parse response JSON: %v", err)
	}

	if res["success"] != true {
		t.Errorf("Expected success=true, got %v", res["success"])
	}
}

func TestOnDemandSyncTargets(t *testing.T) {
	// 1. Visão Principal (114 itens)
	mainTargets := generateMainViewTargets()
	if len(mainTargets) != 114 {
		t.Errorf("Esperado 114 targets para visão principal, obtido: %d", len(mainTargets))
	}

	// 2. Envios Auxiliares (96 itens por auxiliar)
	auxTargets := generateAuxSendsTargets(0) // AUX 1
	if len(auxTargets) != 96 {
		t.Errorf("Esperado 96 targets para envios do Aux 1, obtido: %d", len(auxTargets))
	}

	// 3. Rack de Efeitos Canal de Entrada (Att + EQ 15 + Comp 8 + Gate 6 = 30)
	chTargets := generateChannelFXTargets("CH01")
	if len(chTargets) != 30 {
		t.Errorf("Esperado 30 targets para rack de efeitos de canal, obtido: %d", len(chTargets))
	}

	// 4. Rack de Efeitos Master Stereo (EQ 15 + Comp 8 = 23)
	masterTargets := generateChannelFXTargets("MASTER")
	if len(masterTargets) != 23 {
		t.Errorf("Esperado 23 targets para master out, obtido: %d", len(masterTargets))
	}

	// 5. Rack de Efeitos Aux Master (EQ 15)
	auxFxTargets := generateChannelFXTargets("AUX1")
	if len(auxFxTargets) != 15 {
		t.Errorf("Esperado 15 targets para aux master out, obtido: %d", len(auxFxTargets))
	}
}
