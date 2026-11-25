package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// setupTestPresetEnv cria ambiente isolado para testes de presets
func setupTestPresetEnv(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "channel_preset_test_*")
	if err != nil {
		t.Fatalf("Falha ao criar diretório temporário para testes de preset: %v", err)
	}

	InitSceneManager(tmpDir)
	return tmpDir
}

func cleanupTestPresetEnv(tmpDir string) {
	StopAutoSave()
	_ = os.RemoveAll(tmpDir)
}

func TestChannelPresetSaveAndLoad(t *testing.T) {
	tmpDir := setupTestPresetEnv(t)
	defer cleanupTestPresetEnv(tmpDir)

	rack := ChannelRackData{
		EQ: EQState{
			On:         1,
			Attenuator: -12,
			HPFOn:      1,
			Low:        EQBand{Q: 44, Freq: 25, Gain: 0},
			LowMid:     EQBand{Q: 20, Freq: 50, Gain: -30},
			HiMid:      EQBand{Q: 20, Freq: 80, Gain: 25},
			High:       EQBand{Q: 23, Freq: 105, Gain: 15},
		},
		Comp: CompState{
			On:        1,
			Position:  1,
			Threshold: -260,
			Ratio:     5,
			Attack:    15,
			Release:   45,
			Knee:      2,
			Gain:      30,
		},
		Gate: GateState{
			On:        1,
			Threshold: 310,
			Range:     45,
			Attack:    2,
			Hold:      25,
			Decay:     16,
		},
	}

	preset, err := SaveChannelPreset("Vocal Principal Teste", "Microfone Condensador Beta 87A", "Operador de Som", rack)
	if err != nil {
		t.Fatalf("Erro ao salvar preset de canal: %v", err)
	}

	if preset.ID != "vocal-principal-teste" {
		t.Errorf("Esperado ID 'vocal-principal-teste', obtido: '%s'", preset.ID)
	}

	loaded, err := GetChannelPreset(preset.ID)
	if err != nil {
		t.Fatalf("Erro ao recuperar preset de canal: %v", err)
	}

	if loaded.Name != "Vocal Principal Teste" {
		t.Errorf("Esperado nome 'Vocal Principal Teste', obtido: '%s'", loaded.Name)
	}
	if loaded.Rack.EQ.LowMid.Gain != -30 {
		t.Errorf("Esperado EQ LowMid Gain -30, obtido: %d", loaded.Rack.EQ.LowMid.Gain)
	}
	if loaded.Rack.Comp.Threshold != -260 {
		t.Errorf("Esperado Comp Threshold -260, obtido: %d", loaded.Rack.Comp.Threshold)
	}
	if loaded.Rack.Gate.Range != 45 {
		t.Errorf("Esperado Gate Range 45, obtido: %d", loaded.Rack.Gate.Range)
	}

	// Listagem
	list, err := ListChannelPresets()
	if err != nil {
		t.Fatalf("Erro ao listar presets: %v", err)
	}

	found := false
	for _, p := range list {
		if p.ID == preset.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Preset recém-salvo não foi encontrado na listagem")
	}

	// Exclusão
	if err := DeleteChannelPreset(preset.ID); err != nil {
		t.Fatalf("Erro ao excluir preset: %v", err)
	}

	if _, err := GetChannelPreset(preset.ID); err == nil {
		t.Errorf("Esperava erro ao buscar preset excluído, mas foi encontrado")
	}
}

func TestChannelPresetApplyToChannel(t *testing.T) {
	tmpDir := setupTestPresetEnv(t)
	defer cleanupTestPresetEnv(tmpDir)

	targetCh := 4 // Canal 4 (índice 3)
	chIdx := targetCh - 1

	// Configura canal com volume, mute, pan e sends específicos antes da aplicação
	mixerStateLock.Lock()
	currentMixer.Inputs[chIdx].Fader = 780
	currentMixer.Inputs[chIdx].On = 0 // Mutado
	currentMixer.Inputs[chIdx].Pan = -25
	currentMixer.Inputs[chIdx].AuxSends[0].Level = 600
	currentMixer.Inputs[chIdx].AuxSends[0].On = 1
	currentMixer.Inputs[chIdx].AuxSends[0].Pre = 1 // Pre-fader
	mixerStateLock.Unlock()

	rack := ChannelRackData{
		EQ: EQState{
			On:         1,
			Attenuator: 15,
			LowMid:     EQBand{Gain: 40},
		},
		Comp: CompState{
			On:        1,
			Threshold: -150,
			Ratio:     8,
		},
		Gate: GateState{
			On:        1,
			Threshold: 400,
		},
	}

	preset, err := SaveChannelPreset("Preset Aplicacao", "Teste de aplicacao", "Op", rack)
	if err != nil {
		t.Fatalf("Falha ao salvar preset: %v", err)
	}

	// Aplica no canal 4
	applied, err := ApplyChannelPresetToChannel(preset.ID, targetCh, "Op", 0)
	if err != nil {
		t.Fatalf("Falha ao aplicar preset no canal: %v", err)
	}
	if applied.ID != preset.ID {
		t.Errorf("Esperado ID aplicado '%s', obtido: '%s'", preset.ID, applied.ID)
	}

	mixerStateLock.RLock()
	chState := currentMixer.Inputs[chIdx]
	mixerStateLock.RUnlock()

	// 1. Verifica se os parâmetros de RACK foram atualizados
	if chState.EQ.Attenuator != 15 {
		t.Errorf("Esperado Attenuator 15, obtido: %d", chState.EQ.Attenuator)
	}
	if chState.EQ.LowMid.Gain != 40 {
		t.Errorf("Esperado LowMid Gain 40, obtido: %d", chState.EQ.LowMid.Gain)
	}
	if chState.Comp.Threshold != -150 {
		t.Errorf("Esperado Comp Threshold -150, obtido: %d", chState.Comp.Threshold)
	}
	if chState.Gate.Threshold != 400 {
		t.Errorf("Esperado Gate Threshold 400, obtido: %d", chState.Gate.Threshold)
	}

	// 2. CRÍTICO: Valida que volumes, mutes, pan e aux sends (pre/post/level) NÃO foram alterados
	if chState.Fader != 780 {
		t.Errorf("ERRO: Volume do fader foi modificado! Esperado 780, obtido: %d", chState.Fader)
	}
	if chState.On != 0 {
		t.Errorf("ERRO: Estado de mute foi modificado! Esperado 0, obtido: %d", chState.On)
	}
	if chState.Pan != -25 {
		t.Errorf("ERRO: Pan foi modificado! Esperado -25, obtido: %d", chState.Pan)
	}
	if chState.AuxSends[0].Level != 600 {
		t.Errorf("ERRO: Nível de aux send foi modificado! Esperado 600, obtido: %d", chState.AuxSends[0].Level)
	}
	if chState.AuxSends[0].Pre != 1 {
		t.Errorf("ERRO: Estado de pre/post foi modificado! Esperado 1, obtido: %d", chState.AuxSends[0].Pre)
	}
}

func TestChannelPresetRBAC(t *testing.T) {
	tmpDir := setupTestPresetEnv(t)
	defer cleanupTestPresetEnv(tmpDir)

	adminSess := createSession(User{Username: "admin", Name: "Admin", Role: "ADMIN"})
	opSess := createSession(User{Username: "operador", Name: "Operador", Role: "OPERATOR"})
	basicSess := createSession(User{Username: "basico", Name: "Basico", Role: "BASIC"})
	performerSess := createSession(User{Username: "performer", Name: "Performer", Role: "PERFORMER"})

	basePreset, err := SaveChannelPreset("Preset RBAC Base", "Descricao", "Admin", ChannelRackData{})
	if err != nil {
		t.Fatalf("Erro ao criar preset base: %v", err)
	}

	testCases := []struct {
		name         string
		role         string
		sessionToken string
		canSave      bool
		canApply     bool
		canDelete    bool
	}{
		{"Anonimo", "NONE", "", false, false, false},
		{"Performer", "PERFORMER", performerSess, false, false, false},
		{"Basic", "BASIC", basicSess, false, false, false},
		{"Operator", "OPERATOR", opSess, true, true, true},
		{"Admin", "ADMIN", adminSess, true, true, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// 1. TESTE SALVAR (POST /api/channel-presets)
			saveBody := fmt.Sprintf(`{"name":"Preset %s","channel":1}`, tc.role)
			reqSave := httptest.NewRequest(http.MethodPost, "/api/channel-presets", bytes.NewBufferString(saveBody))
			if tc.sessionToken != "" {
				reqSave.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tc.sessionToken})
			}
			rrSave := httptest.NewRecorder()
			handleAPIChannelPresets(rrSave, reqSave)

			if tc.role == "NONE" {
				if rrSave.Code != http.StatusUnauthorized {
					t.Errorf("[%s] Esperado 401 Unauthorized para salvar preset, obtido %d", tc.name, rrSave.Code)
				}
			} else if tc.canSave {
				if rrSave.Code != http.StatusCreated {
					t.Errorf("[%s] Esperado 201 Created para salvar preset, obtido %d: %s", tc.name, rrSave.Code, rrSave.Body.String())
				}
			} else {
				if rrSave.Code != http.StatusForbidden {
					t.Errorf("[%s] Esperado 403 Forbidden para salvar preset, obtido %d: %s", tc.name, rrSave.Code, rrSave.Body.String())
				}
			}

			// 2. TESTE CARREGAR / APLICAR (POST /api/channel-presets/{id}/apply)
			applyBody := `{"channel":2}`
			reqApply := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/channel-presets/%s/apply", basePreset.ID), bytes.NewBufferString(applyBody))
			if tc.sessionToken != "" {
				reqApply.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tc.sessionToken})
			}
			rrApply := httptest.NewRecorder()
			handleAPIChannelPresetDetail(rrApply, reqApply)

			if tc.role == "NONE" {
				if rrApply.Code != http.StatusUnauthorized {
					t.Errorf("[%s] Esperado 401 Unauthorized para aplicar preset, obtido %d", tc.name, rrApply.Code)
				}
			} else if tc.canApply {
				if rrApply.Code != http.StatusOK {
					t.Errorf("[%s] Esperado 200 OK para aplicar preset, obtido %d: %s", tc.name, rrApply.Code, rrApply.Body.String())
				}
			} else {
				if rrApply.Code != http.StatusForbidden {
					t.Errorf("[%s] Esperado 403 Forbidden para aplicar preset, obtido %d: %s", tc.name, rrApply.Code, rrApply.Body.String())
				}
			}

			// 3. TESTE EXCLUIR (DELETE /api/channel-presets/{id})
			tempP, _ := SaveChannelPreset(fmt.Sprintf("Temp %s", tc.role), "", "test", ChannelRackData{})
			reqDel := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/channel-presets/%s", tempP.ID), nil)
			if tc.sessionToken != "" {
				reqDel.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tc.sessionToken})
			}
			rrDel := httptest.NewRecorder()
			handleAPIChannelPresetDetail(rrDel, reqDel)

			if tc.role == "NONE" {
				if rrDel.Code != http.StatusUnauthorized {
					t.Errorf("[%s] Esperado 401 Unauthorized para deletar preset, obtido %d", tc.name, rrDel.Code)
				}
			} else if tc.canDelete {
				if rrDel.Code != http.StatusOK {
					t.Errorf("[%s] Esperado 200 OK para deletar preset, obtido %d: %s", tc.name, rrDel.Code, rrDel.Body.String())
				}
			} else {
				if rrDel.Code != http.StatusForbidden {
					t.Errorf("[%s] Esperado 403 Forbidden para deletar preset, obtido %d: %s", tc.name, rrDel.Code, rrDel.Body.String())
				}
			}
		})
	}
}

func TestChannelPresetImportExport(t *testing.T) {
	tmpDir := setupTestPresetEnv(t)
	defer cleanupTestPresetEnv(tmpDir)

	opSess := createSession(User{Username: "operador", Name: "Operador", Role: "OPERATOR"})

	presetData := ChannelPreset{
		Version:     1,
		Name:        "Preset Importado JSON",
		Description: "Descrição do import",
		Rack: ChannelRackData{
			EQ: EQState{
				On: 1,
				LowMid: EQBand{
					Gain: 25,
					Freq: 40,
					Q:    20,
				},
			},
			Comp: CompState{
				On:        1,
				Threshold: -200,
			},
			Gate: GateState{
				On:        1,
				Threshold: 350,
			},
		},
	}

	payload, _ := json.Marshal(presetData)

	reqImport := httptest.NewRequest(http.MethodPost, "/api/channel-presets/import", bytes.NewReader(payload))
	reqImport.Header.Set("Content-Type", "application/json")
	reqImport.AddCookie(&http.Cookie{Name: sessionCookieName, Value: opSess})
	rrImport := httptest.NewRecorder()
	handleAPIImportChannelPreset(rrImport, reqImport)

	if rrImport.Code != http.StatusCreated {
		t.Fatalf("Esperado 201 Created na importação do preset, obtido %d: %s", rrImport.Code, rrImport.Body.String())
	}

	reqGet := httptest.NewRequest(http.MethodGet, "/api/channel-presets/preset-importado-json?download=1", nil)
	reqGet.AddCookie(&http.Cookie{Name: sessionCookieName, Value: opSess})
	rrGet := httptest.NewRecorder()
	handleAPIChannelPresetDetail(rrGet, reqGet)

	if rrGet.Code != http.StatusOK {
		t.Fatalf("Esperado 200 OK no download do preset, obtido %d", rrGet.Code)
	}

	if disposition := rrGet.Header().Get("Content-Disposition"); disposition == "" {
		t.Errorf("Esperava cabeçalho Content-Disposition para download")
	}
}

