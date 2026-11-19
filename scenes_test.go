package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func setupTestSceneEnv(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "scenes_test_*")
	if err != nil {
		t.Fatalf("Erro ao criar diretório temporário para testes: %v", err)
	}
	setSessionsFile(filepath.Join(tmpDir, "sessions.json"))
	InitSceneManager(tmpDir)
	return tmpDir
}

func cleanupTestSceneEnv(tmpDir string) {
	StopAutoSave()
	setSessionsFile("sessions.json")
	os.RemoveAll(tmpDir)
}

func TestDefaultMixerStateValues(t *testing.T) {
	st := NewDefaultMixerState()

	if st.StereoMaster.Fader != 0 {
		t.Errorf("Esperado fader Stereo Master 0 (-infinito), obtido: %d", st.StereoMaster.Fader)
	}
	if st.StereoMaster.On != 1 {
		t.Errorf("Esperado Stereo Master ON=1, obtido: %d", st.StereoMaster.On)
	}

	for i := 0; i < 8; i++ {
		if st.AuxMasters[i].Fader != 0 {
			t.Errorf("Esperado Aux Master %d fader 0 (-infinito), obtido: %d", i+1, st.AuxMasters[i].Fader)
		}
		if st.AuxMasters[i].On != 1 {
			t.Errorf("Esperado Aux Master %d ON=1, obtido: %d", i+1, st.AuxMasters[i].On)
		}
	}

	for i := 0; i < 32; i++ {
		ch := st.Inputs[i]
		if ch.Channel != i+1 {
			t.Errorf("Esperado canal %d, obtido: %d", i+1, ch.Channel)
		}
		if ch.Fader != 0 {
			t.Errorf("Esperado fader inicial de canal 0, obtido: %d", ch.Fader)
		}
		if ch.Pan != 0 {
			t.Errorf("Esperado pan inicial 0 (Centro, escala -63 a +63), obtido: %d", ch.Pan)
		}
		if len(ch.AuxSends) != 8 {
			t.Fatalf("Esperado 8 aux sends, obtido: %d", len(ch.AuxSends))
		}
	}
}

func TestApplyParamToState(t *testing.T) {
	tmpDir := setupTestSceneEnv(t)
	defer cleanupTestSceneEnv(tmpDir)

	// Teste Input Fader (0x1C)
	ApplyParamToState(0x1C, 0x00, 2, 750) // CH 3
	st := GetCurrentMixerState()
	if st.Inputs[2].Fader != 750 {
		t.Errorf("Esperado fader CH3 = 750, obtido: %d", st.Inputs[2].Fader)
	}

	// Teste Input ON/MUTE (0x1A)
	ApplyParamToState(0x1A, 0x00, 2, 0) // Mute CH 3
	st = GetCurrentMixerState()
	if st.Inputs[2].On != 0 {
		t.Errorf("Esperado CH3 ON = 0, obtido: %d", st.Inputs[2].On)
	}

	// Teste Input Pan (0x1B)
	ApplyParamToState(0x1B, 0x00, 2, 100)
	st = GetCurrentMixerState()
	if st.Inputs[2].Pan != 100 {
		t.Errorf("Esperado CH3 Pan = 100, obtido: %d", st.Inputs[2].Pan)
	}

	// Teste Input Aux Send Level (0x23, param 0x0B = Aux 4 Level: 3*3 + 2 = 11 = 0x0B)
	ApplyParamToState(0x23, 0x0B, 2, 600)
	st = GetCurrentMixerState()
	if st.Inputs[2].AuxSends[3].Level != 600 {
		t.Errorf("Esperado CH3 Aux4 Send = 600, obtido: %d", st.Inputs[2].AuxSends[3].Level)
	}

	// Teste Input EQ (0x20)
	ApplyParamToState(0x20, 0x0F, 2, 1)  // EQ On (param 0x0F)
	ApplyParamToState(0x20, 0x03, 2, 45) // Low Gain
	st = GetCurrentMixerState()
	if st.Inputs[2].EQ.On != 1 || st.Inputs[2].EQ.Low.Gain != 45 {
		t.Errorf("Esperado CH3 EQ On=1, Low.Gain=45; obtido On=%d, Gain=%d", st.Inputs[2].EQ.On, st.Inputs[2].EQ.Low.Gain)
	}

	// Teste Stereo Master (0x4F, 0x4D)
	ApplyParamToState(0x4F, 0x00, 0x00, 950)
	ApplyParamToState(0x4D, 0x00, 0x00, 0)
	st = GetCurrentMixerState()
	if st.StereoMaster.Fader != 950 || st.StereoMaster.On != 0 {
		t.Errorf("Esperado Master Fader=950, On=0; obtido Fader=%d, On=%d", st.StereoMaster.Fader, st.StereoMaster.On)
	}

	// Teste Aux Master (0x39, 0x36)
	ApplyParamToState(0x39, 0x00, 1, 700) // Aux 2 Master
	ApplyParamToState(0x36, 0x00, 1, 0)
	st = GetCurrentMixerState()
	if st.AuxMasters[1].Fader != 700 || st.AuxMasters[1].On != 0 {
		t.Errorf("Esperado Aux2 Master Fader=700, On=0; obtido Fader=%d, On=%d", st.AuxMasters[1].Fader, st.AuxMasters[1].On)
	}
}

func TestSaveAndLoadScene(t *testing.T) {
	tmpDir := setupTestSceneEnv(t)
	defer cleanupTestSceneEnv(tmpDir)

	// Define um estado conhecido
	ApplyParamToState(0x1C, 0x00, 0, 800) // CH 1 = 800
	SetChannelCustomName(1, "Vocal Principal")

	// Salva a cena
	scene, err := SaveScene("Culto Domingo", "Banda completa", "admin")
	if err != nil {
		t.Fatalf("Erro ao salvar cena: %v", err)
	}

	if scene.ID != "culto-domingo" {
		t.Errorf("Esperado slug 'culto-domingo', obtido: '%s'", scene.ID)
	}
	if scene.State.Inputs[0].Fader != 800 {
		t.Errorf("Esperado fader salvo 800, obtido: %d", scene.State.Inputs[0].Fader)
	}
	if scene.State.Inputs[0].Name != "Vocal Principal" {
		t.Errorf("Esperado nome salvo 'Vocal Principal', obtido: '%s'", scene.State.Inputs[0].Name)
	}

	// Carrega do disco
	loaded, err := GetScene(scene.ID)
	if err != nil {
		t.Fatalf("Erro ao carregar cena salva: %v", err)
	}

	if loaded.Name != "Culto Domingo" {
		t.Errorf("Esperado nome 'Culto Domingo', obtido: '%s'", loaded.Name)
	}
	if loaded.State.Inputs[0].Fader != 800 {
		t.Errorf("Esperado fader carregado 800, obtido: %d", loaded.State.Inputs[0].Fader)
	}

	// Lista de cenas
	scenes, err := ListScenes()
	if err != nil {
		t.Fatalf("Erro ao listar cenas: %v", err)
	}
	if len(scenes) != 1 {
		t.Errorf("Esperado 1 cena na lista, obtido: %d", len(scenes))
	}

	// Exclusão
	if err := DeleteScene(scene.ID); err != nil {
		t.Fatalf("Erro ao excluir cena: %v", err)
	}

	scenesAfter, _ := ListScenes()
	if len(scenesAfter) != 0 {
		t.Errorf("Esperado 0 cenas após exclusão, obtido: %d", len(scenesAfter))
	}
}

func TestAutoSavePersistence(t *testing.T) {
	tmpDir := setupTestSceneEnv(t)
	defer cleanupTestSceneEnv(tmpDir)

	// Altera estado
	ApplyParamToState(0x1C, 0x00, 4, 850) // CH 5

	// Executa flush manual
	FlushStateToDiskIfDirty()

	lastStateFile := filepath.Join(tmpDir, "last_state.json")
	if _, err := os.Stat(lastStateFile); os.IsNotExist(err) {
		t.Fatalf("Arquivo de auto-save '%s' não foi criado", lastStateFile)
	}

	// Cria nova instância do gerenciador para testar carga sem envio MIDI
	InitSceneManager(tmpDir)
	restoredState := GetCurrentMixerState()
	if restoredState.Inputs[4].Fader != 850 {
		t.Errorf("Esperado fader CH5 restaurado = 850, obtido: %d", restoredState.Inputs[4].Fader)
	}
}

func TestGenerateSysExForState(t *testing.T) {
	st := NewDefaultMixerState()
	st.Inputs[0].Fader = 800
	st.Inputs[0].On = 1
	st.StereoMaster.Fader = 823

	msgs := GenerateSysExForState(&st)
	if len(msgs) == 0 {
		t.Fatalf("Nenhuma mensagem SysEx gerada para o estado")
	}

	// Verifica se a quantidade cobre faders, mutes, pans, attenuator, sends, eqs e compressores
	// Stereo (1 fader + 1 on + 13 eq + 8 comp = 23)
	// 8 Aux Masters * (1 fader + 1 on + 13 eq = 15) = 120
	// 32 Canais * (1 fader + 1 on + 1 pan + 1 att + 8*3 sends + 13 eq + 8 comp = 49) = 1568
	// Total esperado: 23 + 120 + 1568 = 1711 mensagens atômicas
	expectedCount := 23 + 8*15 + 32*49
	if len(msgs) != expectedCount {
		t.Errorf("Esperado %d mensagens SysEx para estado completo, obtido: %d", expectedCount, len(msgs))
	}
}

func TestSceneHTTPEndpointsAuth(t *testing.T) {
	tmpDir := setupTestSceneEnv(t)
	defer cleanupTestSceneEnv(tmpDir)

	// Teste requisição sem cookie (deve retornar 401 Unauthorized)
	req := httptest.NewRequest(http.MethodGet, "/api/scenes", nil)
	rr := httptest.NewRecorder()
	handleAPIScenes(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Esperado status 401 para usuário anônimo, obtido: %d", rr.Code)
	}

	// Cria sessão de usuário comum (OPERADOR)
	normalToken := createSession(User{
		Name:     "Operador",
		Username: "operador",
		Role:     "OPERATOR",
	})

	// Operador pode dar GET em /api/scenes
	reqGet := httptest.NewRequest(http.MethodGet, "/api/scenes", nil)
	reqGet.AddCookie(&http.Cookie{Name: sessionCookieName, Value: normalToken})
	rrGet := httptest.NewRecorder()
	handleAPIScenes(rrGet, reqGet)

	if rrGet.Code != http.StatusOK {
		t.Errorf("Esperado status 200 para GET de operador, obtido: %d", rrGet.Code)
	}

	// Operador NÃO pode salvar cena (deve receber 403 Forbidden)
	body := bytes.NewBufferString(`{"name": "Tentativa Operador"}`)
	reqPost := httptest.NewRequest(http.MethodPost, "/api/scenes", body)
	reqPost.AddCookie(&http.Cookie{Name: sessionCookieName, Value: normalToken})
	rrPost := httptest.NewRecorder()
	handleAPIScenes(rrPost, reqPost)

	if rrPost.Code != http.StatusForbidden {
		t.Errorf("Esperado status 403 para POST de operador, obtido: %d", rrPost.Code)
	}

	// ADMIN pode salvar cena com sucesso
	adminToken := createSession(User{
		Name:     "Administrador",
		Username: "admin",
		Role:     "ADMIN",
	})

	bodyAdmin := bytes.NewBufferString(`{"name": "Cena Admin", "description": "Teste via HTTP"}`)
	reqPostAdmin := httptest.NewRequest(http.MethodPost, "/api/scenes", bodyAdmin)
	reqPostAdmin.AddCookie(&http.Cookie{Name: sessionCookieName, Value: adminToken})
	rrPostAdmin := httptest.NewRecorder()
	handleAPIScenes(rrPostAdmin, reqPostAdmin)

	if rrPostAdmin.Code != http.StatusCreated {
		t.Errorf("Esperado status 201 para POST de admin, obtido: %d (body: %s)", rrPostAdmin.Code, rrPostAdmin.Body.String())
	}
}

func TestParamThrottlerTrailingEdge(t *testing.T) {
	pt := newParamThrottler(25*time.Millisecond, 16*time.Millisecond)

	var mu sync.Mutex
	var flushedVals []int

	flushFn := func(v int) {
		mu.Lock()
		flushedVals = append(flushedVals, v)
		mu.Unlock()
	}

	// 1º envio deve passar imediatamente
	midi1, ws1 := pt.allowOrSchedule(0x1C, 0x00, 0x00, 100, flushFn)
	if !midi1 || !ws1 {
		t.Fatalf("Esperado primeiro envio midi=true, ws=true; obtido midi=%v, ws=%v", midi1, ws1)
	}

	// 2º e 3º envios logo em seguida (< 25ms) devem ser retidos no throttle mas agendados para flush final
	midi2, _ := pt.allowOrSchedule(0x1C, 0x00, 0x00, 250, flushFn)
	midi3, _ := pt.allowOrSchedule(0x1C, 0x00, 0x00, 424, flushFn)
	if midi2 || midi3 {
		t.Fatalf("Esperado throttle imediato nos envios 2 e 3; obtido midi2=%v, midi3=%v", midi2, midi3)
	}

	// Aguarda janela do timer de trailing-edge
	time.Sleep(40 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(flushedVals) != 1 || flushedVals[0] != 424 {
		t.Errorf("Esperado flush final com o último valor [424], obtido: %v", flushedVals)
	}
}
