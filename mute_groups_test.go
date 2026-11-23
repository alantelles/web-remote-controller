package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMuteGroupsOverlappingChannels(t *testing.T) {
	tmpDir := setupTestSceneEnv(t)
	defer cleanupTestSceneEnv(tmpDir)

	// Garante que todos os 32 canais começam desmutados (On = 1)
	for ch := 0; ch < 32; ch++ {
		ApplyParamToState(0x1A, 0x00, byte(ch), 1)
	}

	// Configura 3 Mute Groups com canais sobrepostos:
	// Grupo 1 (Banda): CH 1, CH 2, CH 3
	// Grupo 2 (Vozes): CH 2, CH 3, CH 4
	// Grupo 3 (Efeitos): CH 3, CH 5
	if _, err := UpdateMuteGroupConfig(1, "Banda", []int{1, 2, 3}); err != nil {
		t.Fatalf("Erro ao configurar Mute Group 1: %v", err)
	}
	if _, err := UpdateMuteGroupConfig(2, "Vozes", []int{2, 3, 4}); err != nil {
		t.Fatalf("Erro ao configurar Mute Group 2: %v", err)
	}
	if _, err := UpdateMuteGroupConfig(3, "Efeitos", []int{3, 5}); err != nil {
		t.Fatalf("Erro ao configurar Mute Group 3: %v", err)
	}

	// 1. Ativa o Mute Group 1 -> CH 1, 2, 3 devem ser mutados (On = 0); CH 4, 5 continuam ON (1)
	g1, _, err := SetMuteGroupState(1, true)
	if err != nil || !g1.Muted {
		t.Fatalf("Erro ao ativar Mute Group 1: %v", err)
	}

	state := GetCurrentMixerState()
	for _, ch := range []int{1, 2, 3} {
		if state.Inputs[ch-1].On != 0 {
			t.Errorf("Esperava CH %02d mutado (On=0) após ativar Grupo 1, obteve On=%d", ch, state.Inputs[ch-1].On)
		}
	}
	for _, ch := range []int{4, 5} {
		if state.Inputs[ch-1].On != 1 {
			t.Errorf("Esperava CH %02d ativo (On=1), obteve On=%d", ch, state.Inputs[ch-1].On)
		}
	}

	// 2. Ativa o Mute Group 2 -> CH 2, 3, 4 mutados (On = 0)
	if _, _, err := SetMuteGroupState(2, true); err != nil {
		t.Fatalf("Erro ao ativar Mute Group 2: %v", err)
	}
	state = GetCurrentMixerState()
	for _, ch := range []int{1, 2, 3, 4} {
		if state.Inputs[ch-1].On != 0 {
			t.Errorf("Esperava CH %02d mutado (On=0) com Grupos 1 e 2 ativos, obteve On=%d", ch, state.Inputs[ch-1].On)
		}
	}

	// 3. Ativa o Mute Group 3 -> CH 3, 5 mutados (On = 0)
	if _, _, err := SetMuteGroupState(3, true); err != nil {
		t.Fatalf("Erro ao ativar Mute Group 3: %v", err)
	}
	state = GetCurrentMixerState()
	for _, ch := range []int{1, 2, 3, 4, 5} {
		if state.Inputs[ch-1].On != 0 {
			t.Errorf("Esperava CH %02d mutado (On=0) com Grupos 1, 2 e 3 ativos, obteve On=%d", ch, state.Inputs[ch-1].On)
		}
	}

	// 4. Desativa o Mute Group 1:
	// - CH 1 pertence APENAS ao Grupo 1 -> DEVE ser desmutado (On = 1)
	// - CH 2 pertence também ao Grupo 2 (ainda ativo) -> NÃO deve ser desmutado (continua On = 0)
	// - CH 3 pertence também aos Grupos 2 e 3 (ainda ativos) -> NÃO deve ser desmutado (continua On = 0)
	if _, _, err := SetMuteGroupState(1, false); err != nil {
		t.Fatalf("Erro ao desativar Mute Group 1: %v", err)
	}
	state = GetCurrentMixerState()
	if state.Inputs[0].On != 1 {
		t.Errorf("Esperava CH 01 desmutado (On=1) ao desativar Grupo 1, obteve On=%d", state.Inputs[0].On)
	}
	if state.Inputs[1].On != 0 {
		t.Errorf("CH 02 NÃO deveria ser desmutado pois pertence ao Grupo 2 (ativo), mas obteve On=%d", state.Inputs[1].On)
	}
	if state.Inputs[2].On != 0 {
		t.Errorf("CH 03 NÃO deveria ser desmutado pois pertence aos Grupos 2 e 3 (ativos), mas obteve On=%d", state.Inputs[2].On)
	}

	// 5. Desativa o Mute Group 2:
	// - CH 2 e CH 4 não têm mais nenhum grupo ativo -> DEVEM ser desmutados (On = 1)
	// - CH 3 ainda pertence ao Grupo 3 (ativo) -> NÃO deve ser desmutado (continua On = 0)
	if _, _, err := SetMuteGroupState(2, false); err != nil {
		t.Fatalf("Erro ao desativar Mute Group 2: %v", err)
	}
	state = GetCurrentMixerState()
	if state.Inputs[1].On != 1 {
		t.Errorf("Esperava CH 02 desmutado (On=1) após desativar Grupos 1 e 2, obteve On=%d", state.Inputs[1].On)
	}
	if state.Inputs[3].On != 1 {
		t.Errorf("Esperava CH 04 desmutado (On=1) após desativar Grupo 2, obteve On=%d", state.Inputs[3].On)
	}
	if state.Inputs[2].On != 0 {
		t.Errorf("CH 03 NÃO deveria ser desmutado pois ainda pertence ao Grupo 3 (ativo), mas obteve On=%d", state.Inputs[2].On)
	}

	// 6. Operador desmuta manualmente o CH 05 enquanto o Grupo 3 está ativo (permitido)
	ApplyParamToState(0x1A, 0x00, 4, 1) // CH 05 = On
	state = GetCurrentMixerState()
	if state.Inputs[4].On != 1 {
		t.Errorf("Esperava permitir desmute manual do CH 05 mesmo com Grupo 3 ativo, obteve On=%d", state.Inputs[4].On)
	}

	// 7. Desativa o Mute Group 3 -> CH 3 e CH 5 ficam desmutados (On = 1)
	if _, _, err := SetMuteGroupState(3, false); err != nil {
		t.Fatalf("Erro ao desativar Mute Group 3: %v", err)
	}
	state = GetCurrentMixerState()
	for _, ch := range []int{1, 2, 3, 4, 5} {
		if state.Inputs[ch-1].On != 1 {
			t.Errorf("Esperava CH %02d desmutado (On=1) após todos os grupos serem desativados, obteve On=%d", ch, state.Inputs[ch-1].On)
		}
	}
}

func TestLiveEditActiveMuteGroup(t *testing.T) {
	tmpDir := setupTestSceneEnv(t)
	defer cleanupTestSceneEnv(tmpDir)

	for ch := 0; ch < 32; ch++ {
		ApplyParamToState(0x1A, 0x00, byte(ch), 1)
	}

	// Grupo 1 com CH 1, 2 e Grupo 2 com CH 2, 3 — ambos ativos
	_, _ = UpdateMuteGroupConfig(1, "MUTE 1", []int{1, 2})
	_, _ = UpdateMuteGroupConfig(2, "MUTE 2", []int{2, 3})
	_, _, _ = SetMuteGroupState(1, true)
	_, _, _ = SetMuteGroupState(2, true)

	// Edita Grupo 1 (que está ativo): remove CH 1 e CH 2, adiciona CH 6
	// - CH 1 saiu do Grupo 1 e não pertence a outro ativo -> deve desmutar (On = 1)
	// - CH 2 saiu do Grupo 1 mas pertence ao Grupo 2 (ativo) -> deve continuar mutado (On = 0)
	// - CH 6 entrou no Grupo 1 (ativo) -> deve mutar imediatamente (On = 0)
	if _, err := UpdateMuteGroupConfig(1, "MUTE 1 Editado", []int{6}); err != nil {
		t.Fatalf("Erro ao editar Grupo 1 ativo: %v", err)
	}

	state := GetCurrentMixerState()
	if state.Inputs[0].On != 1 {
		t.Errorf("Esperava CH 01 desmutado ao ser removido do Grupo 1 ativo, obteve On=%d", state.Inputs[0].On)
	}
	if state.Inputs[1].On != 0 {
		t.Errorf("Esperava CH 02 continuar mutado pelo Grupo 2 ativo, obteve On=%d", state.Inputs[1].On)
	}
	if state.Inputs[5].On != 0 {
		t.Errorf("Esperava CH 06 mutado ao ser adicionado ao Grupo 1 ativo, obteve On=%d", state.Inputs[5].On)
	}
}

func TestMuteGroupsRBACAndHTTPEndpoints(t *testing.T) {
	tmpDir := setupTestSceneEnv(t)
	defer cleanupTestSceneEnv(tmpDir)

	for ch := 0; ch < 32; ch++ {
		ApplyParamToState(0x1A, 0x00, byte(ch), 1)
	}

	adminToken := createSession(User{Name: "Admin", Username: "admin", Role: "ADMIN"})
	opToken := createSession(User{Name: "Operador", Username: "op1", Role: "OPERATOR"})
	basicToken := createSession(User{Name: "Básico", Username: "basic1", Role: "BASIC"})
	perfToken := createSession(User{Name: "Músico", Username: "perf1", Role: "PERFORMER"})

	// 1. Configuração de Mute Group: Negada para OPERATOR, BASIC e PERFORMER (403 Forbidden)
	cfgPayload, _ := json.Marshal(map[string]interface{}{
		"name":     "Bateria",
		"channels": []int{1, 2, 3, 4},
	})

	for _, tc := range []struct {
		role  string
		token string
	}{
		{"OPERATOR", opToken},
		{"BASIC", basicToken},
		{"PERFORMER", perfToken},
	} {
		req := httptest.NewRequest(http.MethodPut, "/api/mute-groups/1", bytes.NewReader(cfgPayload))
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tc.token})
		rr := httptest.NewRecorder()
		handleAPIMuteGroupDetail(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("Esperava 403 Forbidden ao configurar Mute Group com perfil %s, obteve %d", tc.role, rr.Code)
		}
	}

	// 2. Configuração de Mute Group: Permitida para ADMIN (200 OK)
	reqAdminCfg := httptest.NewRequest(http.MethodPut, "/api/mute-groups/1", bytes.NewReader(cfgPayload))
	reqAdminCfg.AddCookie(&http.Cookie{Name: sessionCookieName, Value: adminToken})
	rrAdminCfg := httptest.NewRecorder()
	handleAPIMuteGroupDetail(rrAdminCfg, reqAdminCfg)
	if rrAdminCfg.Code != http.StatusOK {
		t.Fatalf("Esperava 200 OK para ADMIN configurando Mute Group 1, obteve %d (%s)", rrAdminCfg.Code, rrAdminCfg.Body.String())
	}

	// 3. Acionamento (toggle) de Mute Group:
	// - PERFORMER é bloqueado (403 Forbidden)
	reqPerfToggle := httptest.NewRequest(http.MethodPost, "/api/mute-groups/1/toggle", nil)
	reqPerfToggle.AddCookie(&http.Cookie{Name: sessionCookieName, Value: perfToken})
	rrPerfToggle := httptest.NewRecorder()
	handleAPIMuteGroupDetail(rrPerfToggle, reqPerfToggle)
	if rrPerfToggle.Code != http.StatusForbidden {
		t.Errorf("Esperava 403 Forbidden para PERFORMER acionando Mute Group, obteve %d", rrPerfToggle.Code)
	}

	// - BASIC pode ativar o Mute Group 1 (200 OK -> CH 1..4 mutados)
	reqBasicToggle := httptest.NewRequest(http.MethodPost, "/api/mute-groups/1/toggle", nil)
	reqBasicToggle.AddCookie(&http.Cookie{Name: sessionCookieName, Value: basicToken})
	rrBasicToggle := httptest.NewRecorder()
	handleAPIMuteGroupDetail(rrBasicToggle, reqBasicToggle)
	if rrBasicToggle.Code != http.StatusOK {
		t.Fatalf("Esperava 200 OK para BASIC acionando Mute Group 1, obteve %d", rrBasicToggle.Code)
	}
	state := GetCurrentMixerState()
	for ch := 1; ch <= 4; ch++ {
		if state.Inputs[ch-1].On != 0 {
			t.Errorf("Esperava CH %02d mutado após BASIC ativar Grupo 1, obteve On=%d", ch, state.Inputs[ch-1].On)
		}
	}

	// - OPERATOR pode desativar o Mute Group 1 (200 OK -> CH 1..4 desmutados)
	reqOpToggle := httptest.NewRequest(http.MethodPost, "/api/mute-groups/1/toggle", nil)
	reqOpToggle.AddCookie(&http.Cookie{Name: sessionCookieName, Value: opToken})
	rrOpToggle := httptest.NewRecorder()
	handleAPIMuteGroupDetail(rrOpToggle, reqOpToggle)
	if rrOpToggle.Code != http.StatusOK {
		t.Fatalf("Esperava 200 OK para OPERATOR desativando Mute Group 1, obteve %d", rrOpToggle.Code)
	}
	state = GetCurrentMixerState()
	for ch := 1; ch <= 4; ch++ {
		if state.Inputs[ch-1].On != 1 {
			t.Errorf("Esperava CH %02d desmutado após OPERATOR desativar Grupo 1, obteve On=%d", ch, state.Inputs[ch-1].On)
		}
	}
}
