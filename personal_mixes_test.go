package main

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPersonalMixValidationAndExclusivity(t *testing.T) {
	tmpDir := setupTestSceneEnv(t)
	defer cleanupTestSceneEnv(tmpDir)

	testCSV := filepath.Join(tmpDir, "usuarios.csv")
	origCSV := getUsersCSVFile()
	setUsersCSVFile(testCSV)
	defer setUsersCSVFile(origCSV)

	csvContent := `nome,usuario,senha,papel,mix
Administrador,admin,admin,ADMIN,
Vocalista 1,vocal1,senha1,PERFORMER,
Baterista 1,batera1,senha2,PERFORMER,
`
	if err := os.WriteFile(testCSV, []byte(csvContent), 0644); err != nil {
		t.Fatalf("Erro ao criar CSV de teste: %v", err)
	}

	// 1. Configura alguns volumes no AUX 2 (auxIdx = 1 -> param = 1*3+2 = 5)
	ApplyParamToState(0x23, 5, 0, 823) // CH 1 no AUX 2 = 823 (0 dB)
	ApplyParamToState(0x23, 5, 1, 700) // CH 2 no AUX 2 = 700

	// 2. Cria uma Mix válida
	mix1, err := SavePersonalMix("", PersonalMix{
		Name:              "Mix Vocal Principal",
		Description:       "In-Ear Aux 2",
		OutputAux:         2,
		Enabled:           true,
		PerformerUsername: "vocal1",
		Groups: PersonalMixGroups{
			Me:     []int{1, 2},
			Voices: []int{3, 4},
			Others: []int{5, 6, 7, 8},
		},
	})
	if err != nil {
		t.Fatalf("Esperava sucesso ao criar mix válida, obteve erro: %v", err)
	}
	if mix1.ID != "mix-vocal-principal" {
		t.Errorf("Esperava ID 'mix-vocal-principal', obteve '%s'", mix1.ID)
	}
	if mix1.BaseLevels[0] != 823 || mix1.BaseLevels[1] != 700 {
		t.Errorf("Esperava BaseLevels capturados do AUX 2 [823, 700], obteve [%d, %d]", mix1.BaseLevels[0], mix1.BaseLevels[1])
	}

	// 3. Testa exclusividade de canais entre grupos (CH 2 em Eu e em Vozes simultaneamente -> deve falhar)
	_, err = SavePersonalMix("", PersonalMix{
		Name:      "Mix Com Conflito",
		OutputAux: 3,
		Groups: PersonalMixGroups{
			Me:     []int{1, 2},
			Voices: []int{2, 3},
			Others: []int{4},
		},
	})
	if err == nil {
		t.Fatalf("Esperava erro de validação ao colocar o mesmo canal em dois grupos diferentes")
	}

	// 4. Testa tentativa de vincular usuário que NÃO é PERFORMER (ex: admin)
	_, err = SavePersonalMix("", PersonalMix{
		Name:              "Mix Admin Inválida",
		OutputAux:         1,
		PerformerUsername: "admin",
	})
	if err == nil {
		t.Fatalf("Esperava erro ao vincular usuário ADMIN como PERFORMER de uma mix pessoal")
	}

	// 5. Testa regra de 1 Mix por PERFORMER:
	// Vincular 'vocal1' a uma segunda Mix deve desvincular 'vocal1' da primeira Mix automaticamente.
	mix2, err := SavePersonalMix("", PersonalMix{
		Name:              "Mix Reserva Vocal",
		OutputAux:         4,
		Enabled:           true,
		PerformerUsername: "vocal1",
		Groups: PersonalMixGroups{
			Me:     []int{1},
			Voices: []int{2},
			Others: []int{3},
		},
	})
	if err != nil {
		t.Fatalf("Erro ao criar segunda mix: %v", err)
	}
	if mix2.PerformerUsername != "vocal1" {
		t.Errorf("Esperava mix2 vinculada a 'vocal1', obteve '%s'", mix2.PerformerUsername)
	}

	reloadedMix1, err := GetPersonalMix(mix1.ID)
	if err != nil {
		t.Fatalf("Erro ao recarregar mix1: %v", err)
	}
	if reloadedMix1.PerformerUsername != "" {
		t.Errorf("Esperava mix1 desvinculada após transferência do performer, mas obteve '%s'", reloadedMix1.PerformerUsername)
	}

	// Verifica se o CSV foi sincronizado com a nova mix do vocal1
	uVocal, err := authenticateUser("vocal1", "senha1")
	if err != nil {
		t.Fatalf("Erro ao autenticar vocal1: %v", err)
	}
	if uVocal.MixID != mix2.ID {
		t.Errorf("Esperava MixID no CSV atualizado para '%s', obteve '%s'", mix2.ID, uVocal.MixID)
	}
}

func TestPersonalMixHTTPEndpoints(t *testing.T) {
	tmpDir := setupTestSceneEnv(t)
	defer cleanupTestSceneEnv(tmpDir)

	testCSV := filepath.Join(tmpDir, "usuarios.csv")
	origCSV := getUsersCSVFile()
	setUsersCSVFile(testCSV)
	defer setUsersCSVFile(origCSV)

	csvContent := `nome,usuario,senha,papel,mix
Administrador,admin,admin,ADMIN,
Músico Performer,musico1,senha123,PERFORMER,
Operador,op1,op123,OPERATOR,
`
	_ = os.WriteFile(testCSV, []byte(csvContent), 0644)

	adminToken := createSession(User{Name: "Admin", Username: "admin", Role: "ADMIN"})
	opToken := createSession(User{Name: "Operador", Username: "op1", Role: "OPERATOR"})
	perfToken := createSession(User{Name: "Músico", Username: "musico1", Role: "PERFORMER"})

	// 1. OPERATOR não pode criar mix (403 Forbidden)
	payload := map[string]interface{}{
		"name":               "Mix Violão",
		"output_aux":         1,
		"enabled":            true,
		"performer_username": "musico1",
		"groups": map[string]interface{}{
			"me":     []int{1, 2},
			"voices": []int{3, 4},
			"others": []int{5, 6},
		},
	}
	bodyBytes, _ := json.Marshal(payload)

	reqOp := httptest.NewRequest(http.MethodPost, "/api/personal-mixes", bytes.NewReader(bodyBytes))
	reqOp.AddCookie(&http.Cookie{Name: sessionCookieName, Value: opToken})
	rrOp := httptest.NewRecorder()
	handleAPIPersonalMixes(rrOp, reqOp)
	if rrOp.Code != http.StatusForbidden {
		t.Errorf("Esperava 403 Forbidden para OPERATOR criando mix, obteve %d", rrOp.Code)
	}

	// 2. ADMIN cria mix com sucesso (201 Created)
	reqAdmin := httptest.NewRequest(http.MethodPost, "/api/personal-mixes", bytes.NewReader(bodyBytes))
	reqAdmin.AddCookie(&http.Cookie{Name: sessionCookieName, Value: adminToken})
	rrAdmin := httptest.NewRecorder()
	handleAPIPersonalMixes(rrAdmin, reqAdmin)
	if rrAdmin.Code != http.StatusCreated {
		t.Fatalf("Esperava 201 Created para ADMIN, obteve %d (%s)", rrAdmin.Code, rrAdmin.Body.String())
	}

	// 3. PERFORMER consulta /api/personal-mixes e recebe sua própria mix
	reqPerf := httptest.NewRequest(http.MethodGet, "/api/personal-mixes", nil)
	reqPerf.AddCookie(&http.Cookie{Name: sessionCookieName, Value: perfToken})
	rrPerf := httptest.NewRecorder()
	handleAPIPersonalMixes(rrPerf, reqPerf)
	if rrPerf.Code != http.StatusOK {
		t.Fatalf("Esperava 200 OK para PERFORMER em GET /api/personal-mixes, obteve %d", rrPerf.Code)
	}
	var perfResp struct {
		Success bool         `json:"success"`
		Mix     *PersonalMix `json:"mix"`
	}
	if err := json.Unmarshal(rrPerf.Body.Bytes(), &perfResp); err != nil {
		t.Fatalf("Erro ao decodificar resposta do performer: %v", err)
	}
	if perfResp.Mix == nil || perfResp.Mix.ID != "mix-viol-o" {
		t.Errorf("Esperava mix 'mix-viol-o' para o performer, obteve %+v", perfResp.Mix)
	}

	// 4. ADMIN desabilita a mix via PUT /api/personal-mixes/{id}
	payload["enabled"] = false
	putBytes, _ := json.Marshal(payload)
	reqPut := httptest.NewRequest(http.MethodPut, "/api/personal-mixes/"+perfResp.Mix.ID, bytes.NewReader(putBytes))
	reqPut.AddCookie(&http.Cookie{Name: sessionCookieName, Value: adminToken})
	rrPut := httptest.NewRecorder()
	handleAPIPersonalMixDetail(rrPut, reqPut)
	if rrPut.Code != http.StatusOK {
		t.Fatalf("Esperava 200 OK no PUT de atualização da mix, obteve %d", rrPut.Code)
	}

	updatedMix, _ := GetPersonalMix(perfResp.Mix.ID)
	if updatedMix == nil || updatedMix.Enabled != false {
		t.Errorf("Esperava mix desabilitada (Enabled=false), obteve %+v", updatedMix)
	}
}

func TestEqualPowerJoystickCalculationAndMultiplier(t *testing.T) {
	tmpDir := setupTestSceneEnv(t)
	defer cleanupTestSceneEnv(tmpDir)

	testCSV := filepath.Join(tmpDir, "usuarios.csv")
	origCSV := getUsersCSVFile()
	setUsersCSVFile(testCSV)
	defer setUsersCSVFile(origCSV)

	csvContent := `nome,usuario,senha,papel,mix
Administrador,admin,admin,ADMIN,
Cantor,cantor1,senha1,PERFORMER,
`
	_ = os.WriteFile(testCSV, []byte(csvContent), 0644)

	// 1. Verifica conservação de potência acústica (wMe² + wVoices² + wOthers² == 1.0) em diversos pontos do disco
	testPoints := [][2]float64{
		{0, 0},                        // Centro (equilíbrio)
		{0, 1},                        // Pólo EU (100% Eu)
		{-0.8660254037844386, -0.5},   // Pólo VOZES (100% Vozes)
		{0.8660254037844386, -0.5},    // Pólo OUTROS (100% Outros)
		{-0.8660254037844386, +0.5},   // Meio termo entre EU e VOZES (oposto a OUTROS)
		{0.35, -0.22},                 // Ponto intermediário arbitrário
		{2.5, 3.0},                    // Fora do círculo unitário (deve sofrer clamp)
	}

	for _, pt := range testPoints {
		wMe, wVoices, wOthers, cx, cy := CalculateEqualPowerWeights(pt[0], pt[1])
		sumSq := wMe*wMe + wVoices*wVoices + wOthers*wOthers
		if math.Abs(sumSq-1.0) > 1e-6 {
			t.Errorf("Equal Power violado em (%v, %v): wMe²+wVoices²+wOthers² = %f (esperava 1.0)", pt[0], pt[1], sumSq)
		}
		if math.Hypot(cx, cy) > 1.000001 {
			t.Errorf("Esperava coordenada limitada ao círculo unitário, obteve r=%f", math.Hypot(cx, cy))
		}
	}

	// No pólo EU (0, 1), wMe deve ser 1.0 e wVoices = wOthers = 0.0
	wMePole, wVoicesPole, wOthersPole, _, _ := CalculateEqualPowerWeights(0, 1)
	if math.Abs(wMePole-1.0) > 1e-6 || wVoicesPole > 1e-6 || wOthersPole > 1e-6 {
		t.Errorf("No pólo EU (0,1), esperava (1, 0, 0), obteve (%f, %f, %f)", wMePole, wVoicesPole, wOthersPole)
	}

	// 2. Admin ajusta a mixagem base no AUX 1 (param = 0*3 + 2 = 2)
	ApplyParamToState(0x23, 2, 0, 800) // CH 1 (Eu) = 800
	ApplyParamToState(0x23, 2, 1, 600) // CH 2 (Vozes) = 600
	ApplyParamToState(0x23, 2, 2, 400) // CH 3 (Outros) = 400

	mix, err := SavePersonalMix("", PersonalMix{
		Name:              "Mix Cantor",
		OutputAux:         1,
		Enabled:           true,
		PerformerUsername: "cantor1",
		Groups: PersonalMixGroups{
			Me:     []int{1},
			Voices: []int{2},
			Others: []int{3},
		},
	})
	if err != nil {
		t.Fatalf("Erro ao criar mix: %v", err)
	}

	// 3. Performer move joystick para o centro (0, 0) -> cada grupo recebe BaseLevel * √(1/3)
	_, volsCenter, weightsCenter, err := ApplyPersonalMixJoystick(mix.ID, 0, 0, true)
	if err != nil {
		t.Fatalf("Erro ao aplicar joystick no centro: %v", err)
	}
	expectedCh1Center := int(math.Round(800.0 * weightsCenter[0]))
	expectedCh2Center := int(math.Round(600.0 * weightsCenter[1]))
	expectedCh3Center := int(math.Round(400.0 * weightsCenter[2]))

	if volsCenter[1] != expectedCh1Center || volsCenter[2] != expectedCh2Center || volsCenter[3] != expectedCh3Center {
		t.Errorf("Volumes no centro incorretos: obteve %v, esperava [%d, %d, %d]",
			volsCenter, expectedCh1Center, expectedCh2Center, expectedCh3Center)
	}

	// 4. Performer move joystick 100% para o pólo EU (0, 1) -> CH 1 vai a 100% do BaseLevel (800), CH 2 e CH 3 vão a 0
	_, volsMe, _, err := ApplyPersonalMixJoystick(mix.ID, 0, 1, true)
	if err != nil {
		t.Fatalf("Erro ao aplicar joystick no pólo EU: %v", err)
	}
	if volsMe[1] != 800 || volsMe[2] != 0 || volsMe[3] != 0 {
		t.Errorf("No pólo EU esperava volumes [CH1=800, CH2=0, CH3=0], obteve %v", volsMe)
	}

	// Verifica que os BaseLevels configurados pelo Admin NÃO foram destruídos pelo movimento do joystick
	reloaded, _ := GetPersonalMix(mix.ID)
	if reloaded.BaseLevels[0] != 800 || reloaded.BaseLevels[1] != 600 || reloaded.BaseLevels[2] != 400 {
		t.Errorf("BaseLevels não deveriam ser sobrescritos pelo performer! Obteve: %v", reloaded.BaseLevels[:3])
	}

	// 5. Se o Admin alterar o fader de envio do CH 1 no AUX 1 para 900 no console principal, o BaseLevel atualiza automaticamente
	ApplyParamToState(0x23, 2, 0, 900)
	reloadedAfterAdmin, _ := GetPersonalMix(mix.ID)
	if reloadedAfterAdmin.BaseLevels[0] != 900 {
		t.Errorf("Esperava BaseLevel do CH 1 atualizado para 900 após ajuste do Admin no console, obteve %d", reloadedAfterAdmin.BaseLevels[0])
	}
}

