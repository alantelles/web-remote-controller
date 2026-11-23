package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestLoadUsersAndCustomFile(t *testing.T) {
	testCSV := "test_custom_usuarios.csv"
	defer os.Remove(testCSV)

	// Garante que o arquivo não existe previamente
	_ = os.Remove(testCSV)

	// Salva o caminho anterior e restaura ao final
	origFile := getUsersCSVFile()
	defer setUsersCSVFile(origFile)

	// Configura para usar o arquivo customizado
	setUsersCSVFile(testCSV)

	// 1. Testa que ensureUsersCSV cria o arquivo se ele não existir
	ensureUsersCSV()
	if _, err := os.Stat(testCSV); os.IsNotExist(err) {
		t.Fatalf("ensureUsersCSV deveria ter criado o arquivo %s", testCSV)
	}

	// 2. Testa que o arquivo criado possui o usuário padrão admin
	users, err := loadUsers()
	if err != nil {
		t.Fatalf("Erro ao carregar users padrão: %v", err)
	}
	if len(users) == 0 {
		t.Fatalf("Esperava pelo menos 1 usuário padrão no arquivo recém-criado")
	}

	u, err := authenticateUser("admin", "admin")
	if err != nil {
		t.Fatalf("Esperava sucesso na autenticação do admin padrão: %v", err)
	}
	if u.Role != "ADMIN" {
		t.Fatalf("Esperava papel ADMIN, obteve: %s", u.Role)
	}

	// 3. Testa quando o arquivo só tem cabeçalho (0 usuários válidos)
	headerOnly := "nome,usuario,senha,papel\n"
	if err := os.WriteFile(testCSV, []byte(headerOnly), 0644); err != nil {
		t.Fatalf("Erro ao sobrescrever com cabeçalho: %v", err)
	}

	emptyUsers, err := loadUsers()
	if err != nil {
		t.Fatalf("Erro ao carregar arquivo de usuários: %v", err)
	}
	if len(emptyUsers) != 0 {
		t.Fatalf("Esperava 0 usuários para arquivo apenas com cabeçalho, obteve %d", len(emptyUsers))
	}

	// 4. Testa arquivo customizado populado
	customContent := "nome,usuario,senha,papel\nJoão da Silva,jsilva,segredo123,ADMIN\nMaria Som,maria,som456,ADMIN\n"
	if err := os.WriteFile(testCSV, []byte(customContent), 0644); err != nil {
		t.Fatalf("Erro ao escrever conteúdo customizado: %v", err)
	}

	users, err = loadUsers()
	if err != nil || len(users) != 2 {
		t.Fatalf("Esperava 2 usuários, obteve: %d (erro: %v)", len(users), err)
	}

	u, err = authenticateUser("jsilva", "segredo123")
	if err != nil {
		t.Fatalf("Erro ao autenticar jsilva: %v", err)
	}
	if u.Name != "João da Silva" {
		t.Errorf("Esperava 'João da Silva', obteve '%s'", u.Name)
	}

	// Senha incorreta
	_, err = authenticateUser("jsilva", "senhaerrada")
	if err == nil {
		t.Errorf("Esperava erro ao usar senha errada")
	}
}

func TestSessionLifecycleAndPersistence(t *testing.T) {
	testSessions := "test_sessions.json"
	defer os.Remove(testSessions)
	defer os.Remove(testSessions + ".tmp")
	_ = os.Remove(testSessions)

	origSessionsFile := getSessionsFile()
	defer setSessionsFile(origSessionsFile)
	setSessionsFile(testSessions)

	testUser := User{
		Name:     "Test Admin",
		Username: "admin_test",
		Role:     "ADMIN",
	}

	// 1. Criação e persistência em disco
	token := createSession(testUser)
	if token == "" {
		t.Fatalf("Token de sessão veio vazio")
	}

	// Verifica se o arquivo foi criado em disco
	if _, err := os.Stat(testSessions); os.IsNotExist(err) {
		t.Fatalf("Arquivo de sessões '%s' deveria ter sido criado", testSessions)
	}

	// 2. Simula reinicialização do servidor limpando o mapa em memória
	sessionsLock.Lock()
	sessions = make(map[string]*Session)
	sessionsLock.Unlock()

	// Antes de carregar, getSession não encontra
	if sess := getSession(token); sess != nil {
		t.Fatalf("Sessão deveria estar vazia em memória antes de loadSessions")
	}

	// 3. Restaura do arquivo
	loadSessions()
	sess := getSession(token)
	if sess == nil {
		t.Fatalf("Sessão persistida deveria ter sido restaurada pelo loadSessions")
	}
	if sess.User.Username != "admin_test" {
		t.Fatalf("Esperava username 'admin_test', obteve '%s'", sess.User.Username)
	}

	// 4. Exclusão e atualização no arquivo
	deleteSession(token)
	if getSession(token) != nil {
		t.Fatalf("Sessão ainda existe após exclusão")
	}

	// Limpa memória de novo e recarrega para garantir que a exclusão persistiu
	sessionsLock.Lock()
	sessions = make(map[string]*Session)
	sessionsLock.Unlock()

	loadSessions()
	if getSession(token) != nil {
		t.Fatalf("Sessão deletada não deveria ser restaurada do disco")
	}
}

func TestHTTPLoginAndProtectedEndpoints(t *testing.T) {
	testCSV := "test_http_login_usuarios.csv"
	testSess := "test_http_login_sessions.json"
	defer os.Remove(testCSV)
	defer os.Remove(testSess)

	origCSV := getUsersCSVFile()
	origSess := getSessionsFile()
	setUsersCSVFile(testCSV)
	setSessionsFile(testSess)
	defer func() {
		setUsersCSVFile(origCSV)
		setSessionsFile(origSess)
	}()
	ensureUsersCSV()

	// 1. GET /login deve retornar 200 OK
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rr := httptest.NewRecorder()
	handleLogin(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /login: esperava código 200, obteve %d", rr.Code)
	}

	// 2. POST /login com credenciais inválidas via JSON
	invalidBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "senha_incorreta",
	})
	req = httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(invalidBody))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	handleLogin(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("POST /login inválido: esperava 401, obteve %d", rr.Code)
	}

	// 3. POST /login com credenciais válidas via JSON
	validBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "admin",
	})
	req = httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(validBody))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	handleLogin(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("POST /login válido: esperava 200, obteve %d", rr.Code)
	}

	// Recupera o cookie de sessão enviado
	cookies := rr.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("Cookie de sessão não foi setado")
	}

	// 4. GET /api/me sem cookie -> deve retornar 401
	req = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	rr = httptest.NewRecorder()
	handleMe(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/me sem cookie: esperava 401, obteve %d", rr.Code)
	}

	// 5. GET /api/me com cookie -> deve retornar 200 com JSON do usuário
	req = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	handleMe(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /api/me com cookie: esperava 200, obteve %d", rr.Code)
	}
	var meResp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &meResp); err != nil {
		t.Fatalf("Erro ao decodificar resposta de /api/me: %v", err)
	}
	if meResp["role"] != "ADMIN" {
		t.Errorf("Esperava papel ADMIN, obteve %v", meResp["role"])
	}

	// 6. Teste de GET /logout -> limpa cookie e redireciona
	req = httptest.NewRequest(http.MethodGet, "/logout", nil)
	req.AddCookie(sessionCookie)
	rr = httptest.NewRecorder()
	handleLogout(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Errorf("GET /logout: esperava 303 See Other, obteve %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/login" {
		t.Errorf("GET /logout: esperava redirecionamento para /login, obteve %s", loc)
	}

	// 7. GET /ws sem cookie -> deve retornar 401
	req = httptest.NewRequest(http.MethodGet, "/ws", nil)
	rr = httptest.NewRecorder()
	handleWS(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("GET /ws sem autenticação: esperava 401, obteve %d", rr.Code)
	}

	// 8. GET /m sem cookie -> deve redirecionar para /login (303)
	req = httptest.NewRequest(http.MethodGet, "/m", nil)
	rr = httptest.NewRecorder()
	handleMobile(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Errorf("GET /m sem autenticação: esperava 303, obteve %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/login" {
		t.Errorf("GET /m sem autenticação: esperava redirect para /login, obteve %s", loc)
	}

	// 9. GET /m com cookie de sessão válido -> deve retornar 200 OK (serve mobile.html)
	// Cria uma nova sessão para testar o acesso autenticado
	sessToken := createSession(User{Username: "admin", Name: "Admin", Role: "ADMIN"})
	req = httptest.NewRequest(http.MethodGet, "/m", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessToken})
	rr = httptest.NewRecorder()
	handleMobile(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /m autenticado: esperava 200, obteve %d", rr.Code)
	}

	// 10. POST /login com User-Agent Mobile -> deve retornar redirect para "/m"
	mobileBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "admin",
	})
	req = httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(mobileBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1")
	rr = httptest.NewRecorder()
	handleLogin(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("POST /login mobile: esperava 200, obteve %d", rr.Code)
	}
	var loginMobileResp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &loginMobileResp); err != nil {
		t.Fatalf("Erro ao decodificar JSON de login mobile: %v", err)
	}
	if loginMobileResp["redirect"] != "/m" {
		t.Errorf("POST /login mobile: esperava redirect '/m', obteve %v", loginMobileResp["redirect"])
	}
}

func TestSystemShutdownEndpoint(t *testing.T) {
	testFile := "test_shutdown_usuarios.csv"
	testSess := "test_shutdown_sessions.json"
	defer os.Remove(testFile)
	defer os.Remove(testSess)
	origCSV := getUsersCSVFile()
	origSess := getSessionsFile()
	setUsersCSVFile(testFile)
	setSessionsFile(testSess)
	defer func() {
		setUsersCSVFile(origCSV)
		setSessionsFile(origSess)
	}()
	ensureUsersCSV()

	adminToken := createSession(User{
		Name:     "Administrador",
		Username: "admin",
		Role:     "ADMIN",
	})

	// 1. Senha incorreta -> deve retornar 401 Unauthorized
	wrongBody := bytes.NewBufferString(`{"password": "wrongpassword"}`)
	reqWrong := httptest.NewRequest(http.MethodPost, "/api/system/shutdown", wrongBody)
	reqWrong.AddCookie(&http.Cookie{Name: sessionCookieName, Value: adminToken})
	rrWrong := httptest.NewRecorder()
	handleAPISystemShutdown(rrWrong, reqWrong)
	if rrWrong.Code != http.StatusUnauthorized {
		t.Errorf("Esperava 401 para senha incorreta no shutdown, obteve %d", rrWrong.Code)
	}

	// 2. Senha correta, mas sem o parâmetro -podeDesligarServidor (canShutdownServer = false)
	canShutdownServer = false
	okBody := bytes.NewBufferString(`{"password": "admin"}`)
	reqNoFlag := httptest.NewRequest(http.MethodPost, "/api/system/shutdown", okBody)
	reqNoFlag.AddCookie(&http.Cookie{Name: sessionCookieName, Value: adminToken})
	rrNoFlag := httptest.NewRecorder()
	handleAPISystemShutdown(rrNoFlag, reqNoFlag)
	if rrNoFlag.Code != http.StatusOK {
		t.Fatalf("Esperava 200 OK com senha correta sem flag, obteve %d", rrNoFlag.Code)
	}
	var respNoFlag map[string]interface{}
	if err := json.Unmarshal(rrNoFlag.Body.Bytes(), &respNoFlag); err != nil {
		t.Fatalf("Erro ao decodificar JSON: %v", err)
	}
	if respNoFlag["command_received"] != true || respNoFlag["shutdown_executed"] != false {
		t.Errorf("Esperava command_received=true e shutdown_executed=false, obteve: %v", respNoFlag)
	}

	// 3. Senha correta e com -podeDesligarServidor ativo (canShutdownServer = true)
	canShutdownServer = true
	defer func() { canShutdownServer = false }()

	calledChan := make(chan bool, 1)
	origExec := shutdownExecFunc
	shutdownExecFunc = func() {
		calledChan <- true
	}
	defer func() { shutdownExecFunc = origExec }()

	okBody2 := bytes.NewBufferString(`{"password": "admin"}`)
	reqWithFlag := httptest.NewRequest(http.MethodPost, "/api/system/shutdown", okBody2)
	reqWithFlag.AddCookie(&http.Cookie{Name: sessionCookieName, Value: adminToken})
	rrWithFlag := httptest.NewRecorder()
	handleAPISystemShutdown(rrWithFlag, reqWithFlag)
	if rrWithFlag.Code != http.StatusOK {
		t.Fatalf("Esperava 200 OK com senha correta e flag ativa, obteve %d", rrWithFlag.Code)
	}
	var respWithFlag map[string]interface{}
	if err := json.Unmarshal(rrWithFlag.Body.Bytes(), &respWithFlag); err != nil {
		t.Fatalf("Erro ao decodificar JSON: %v", err)
	}
	if respWithFlag["command_received"] != true || respWithFlag["shutdown_executed"] != true {
		t.Errorf("Esperava command_received=true e shutdown_executed=true, obteve: %v", respWithFlag)
	}

	select {
	case <-calledChan:
		// OK: função de desligamento acionada
	case <-time.After(1 * time.Second):
		t.Errorf("Função de desligamento não foi acionada após comando válido com flag ativa")
	}
}

func TestRBACRolesAndLoadUsers(t *testing.T) {
	testCSV := "test_rbac_usuarios.csv"
	defer os.Remove(testCSV)
	origCSV := getUsersCSVFile()
	setUsersCSVFile(testCSV)
	defer setUsersCSVFile(origCSV)

	content := `nome,usuario,senha,papel,mix
Administrador Geral,admin,admin123,ADMIN,
Operador de Mesa,operador,op123,OPERATOR,
Operador Basico,basico,basico123,BASIC,
Musico Vocalista,musico,musico123,PERFORMER,aux1
Musico Baterista,batera,batera123,PERFORMER,
`
	if err := os.WriteFile(testCSV, []byte(content), 0644); err != nil {
		t.Fatalf("Erro ao criar arquivo de teste: %v", err)
	}

	users, err := loadUsers()
	if err != nil {
		t.Fatalf("loadUsers falhou: %v", err)
	}
	if len(users) != 5 {
		t.Fatalf("Esperava 5 usuários, obteve %d", len(users))
	}

	// Verifica admin
	uAdmin, _ := authenticateUser("admin", "admin123")
	if uAdmin == nil || uAdmin.Role != "ADMIN" {
		t.Fatalf("Esperava admin com papel ADMIN, obteve: %+v", uAdmin)
	}

	// Verifica operador
	uOp, _ := authenticateUser("operador", "op123")
	if uOp == nil || uOp.Role != "OPERATOR" {
		t.Fatalf("Esperava operador com papel OPERATOR, obteve: %+v", uOp)
	}

	// Verifica basic
	uBasic, _ := authenticateUser("basico", "basico123")
	if uBasic == nil || uBasic.Role != "BASIC" {
		t.Fatalf("Esperava basic com papel BASIC, obteve: %+v", uBasic)
	}

	// Verifica performer com mix customizada
	uPerformer, _ := authenticateUser("musico", "musico123")
	if uPerformer == nil || uPerformer.Role != "PERFORMER" || uPerformer.MixID != "aux1" {
		t.Fatalf("Esperava musico com papel PERFORMER e mix_id 'aux1', obteve: %+v", uPerformer)
	}

	// Verifica performer sem mix informada (deve cair no fallback "1")
	uBatera, _ := authenticateUser("batera", "batera123")
	if uBatera == nil || uBatera.Role != "PERFORMER" || uBatera.MixID != "1" {
		t.Fatalf("Esperava batera com mix_id fallback '1', obteve: %+v", uBatera)
	}
}

func TestPerformerRedirectAndMixEndpoint(t *testing.T) {
	testCSV := "test_performer_usuarios.csv"
	testSess := "test_performer_sessions.json"
	defer os.Remove(testCSV)
	defer os.Remove(testSess)

	origCSV := getUsersCSVFile()
	origSess := getSessionsFile()
	setUsersCSVFile(testCSV)
	setSessionsFile(testSess)
	defer func() {
		setUsersCSVFile(origCSV)
		setSessionsFile(origSess)
	}()

	csvContent := `nome,usuario,senha,papel,mix
Cantor Principal,cantor,senha123,PERFORMER,vocal1
Operador,operador,op123,OPERATOR,
`
	if err := os.WriteFile(testCSV, []byte(csvContent), 0644); err != nil {
		t.Fatalf("Erro ao criar CSV de teste: %v", err)
	}

	// 1. Login JSON com PERFORMER -> deve retornar redirect para "/mix?id=vocal1"
	body, _ := json.Marshal(map[string]string{
		"username": "cantor",
		"password": "senha123",
	})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handleLogin(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Esperava 200 OK no login de cantor, obteve %d", rr.Code)
	}
	var loginResp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("Erro ao decodificar resposta JSON: %v", err)
	}
	if loginResp["redirect"] != "/mix?id=vocal1" {
		t.Errorf("Esperava redirect para '/mix?id=vocal1', obteve: %v", loginResp["redirect"])
	}

	// Recupera cookie
	cookies := rr.Result().Cookies()
	var sessCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			sessCookie = c
			break
		}
	}
	if sessCookie == nil {
		t.Fatalf("Cookie de sessão não retornado")
	}

	// 2. GET /login com sessão já autenticada de PERFORMER -> redirect 303 para "/mix?id=vocal1"
	reqGetLogin := httptest.NewRequest(http.MethodGet, "/login", nil)
	reqGetLogin.AddCookie(sessCookie)
	rrGetLogin := httptest.NewRecorder()
	handleLogin(rrGetLogin, reqGetLogin)
	if rrGetLogin.Code != http.StatusSeeOther {
		t.Errorf("Esperava 303 no GET /login de performer autenticado, obteve %d", rrGetLogin.Code)
	}
	if loc := rrGetLogin.Header().Get("Location"); loc != "/mix?id=vocal1" {
		t.Errorf("Esperava redirect para '/mix?id=vocal1', obteve %s", loc)
	}

	// 3. GET /m com sessão de PERFORMER -> redirect para "/mix?id=vocal1"
	reqMobile := httptest.NewRequest(http.MethodGet, "/m", nil)
	reqMobile.AddCookie(sessCookie)
	rrMobile := httptest.NewRecorder()
	handleMobile(rrMobile, reqMobile)
	if rrMobile.Code != http.StatusSeeOther {
		t.Errorf("Esperava 303 no GET /m para performer, obteve %d", rrMobile.Code)
	}
	if loc := rrMobile.Header().Get("Location"); loc != "/mix?id=vocal1" {
		t.Errorf("Esperava redirect para '/mix?id=vocal1', obteve %s", loc)
	}

	// 4. GET /mix sem autenticação -> redirect para /login
	reqAnonMix := httptest.NewRequest(http.MethodGet, "/mix", nil)
	rrAnonMix := httptest.NewRecorder()
	handleMix(rrAnonMix, reqAnonMix)
	if rrAnonMix.Code != http.StatusSeeOther {
		t.Errorf("Esperava 303 no GET /mix anônimo, obteve %d", rrAnonMix.Code)
	}

	// 5. GET /mix autenticado -> 200 OK (serve mix.html)
	reqAuthMix := httptest.NewRequest(http.MethodGet, "/mix?id=vocal1", nil)
	reqAuthMix.AddCookie(sessCookie)
	rrAuthMix := httptest.NewRecorder()
	handleMix(rrAuthMix, reqAuthMix)
	if rrAuthMix.Code != http.StatusOK {
		t.Errorf("Esperava 200 OK no GET /mix autenticado, obteve %d", rrAuthMix.Code)
	}
	if !bytes.Contains(rrAuthMix.Body.Bytes(), []byte("It works!")) {
		t.Errorf("Esperava conteúdo 'It works!' na resposta de mix.html")
	}

	// 6. GET /api/me para PERFORMER retorna mix_id
	reqMe := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	reqMe.AddCookie(sessCookie)
	rrMe := httptest.NewRecorder()
	handleMe(rrMe, reqMe)
	var meData map[string]interface{}
	_ = json.Unmarshal(rrMe.Body.Bytes(), &meData)
	if meData["role"] != "PERFORMER" || meData["mix_id"] != "vocal1" {
		t.Errorf("Esperava role=PERFORMER e mix_id=vocal1, obteve: %v", meData)
	}
}

func TestIsVolumeOrMuteParam(t *testing.T) {
	// Casos que DEVEM ser aceitos para o perfil BASIC (apenas volumes e mute)
	allowed := []struct {
		elem  byte
		param byte
		desc  string
	}{
		{0x1C, 0x00, "Input Fader"},
		{0x1A, 0x00, "Input ON/MUTE"},
		{0x4F, 0x00, "Stereo Master Fader"},
		{0x4D, 0x00, "Stereo Master ON"},
		{0x4B, 0x00, "Stereo Master ON alt"},
		{0x39, 0x00, "Aux Master Fader"},
		{0x31, 0x00, "Aux Master Fader alt"},
		{0x36, 0x00, "Aux Master ON"},
		{0x2F, 0x00, "Aux Master ON alt"},
		{0x23, 0x00, "Aux 1 Send ON (pp=0)"},
		{0x23, 0x02, "Aux 1 Send Level (pp=2)"},
		{0x23, 0x03, "Aux 2 Send ON (pp=3)"},
		{0x23, 0x05, "Aux 2 Send Level (pp=5)"},
		{0x23, 0x17, "Aux 8 Send Level (pp=23)"},
	}

	for _, tc := range allowed {
		if !isVolumeOrMuteParam(tc.elem, tc.param) {
			t.Errorf("Esperava permissão para %s (elem 0x%02X, param 0x%02X)", tc.desc, tc.elem, tc.param)
		}
	}

	// Casos que DEVEM ser bloqueados para o perfil BASIC
	blocked := []struct {
		elem  byte
		param byte
		desc  string
	}{
		{0x1B, 0x00, "Input Pan"},
		{0x1D, 0x00, "Input Attenuator"},
		{0x20, 0x00, "Input EQ Param"},
		{0x21, 0x00, "Input Gate Param"},
		{0x1F, 0x00, "Input Comp Param"},
		{0x22, 0x00, "Input Comp Param alt"},
		{0x35, 0x00, "Aux EQ Param"},
		{0x50, 0x00, "Stereo EQ Param"},
		{0x51, 0x00, "Stereo Comp Param"},
		{0x23, 0x01, "Aux 1 Send Pre/Post (pp=1)"},
		{0x23, 0x04, "Aux 2 Send Pre/Post (pp=4)"},
	}

	for _, tc := range blocked {
		if isVolumeOrMuteParam(tc.elem, tc.param) {
			t.Errorf("Esperava bloqueio para %s (elem 0x%02X, param 0x%02X)", tc.desc, tc.elem, tc.param)
		}
	}
}

func TestAPISyncMixerRBAC(t *testing.T) {
	// Cria sessões com diferentes papéis
	tokenAdmin := createSession(User{Username: "admin_test", Role: "ADMIN"})
	tokenOp := createSession(User{Username: "op_test", Role: "OPERATOR"})
	tokenBasic := createSession(User{Username: "basic_test", Role: "BASIC"})

	// 1. Operador não pode fazer sincronização geral -> 403 Forbidden
	reqOp := httptest.NewRequest(http.MethodPost, "/api/state/sync-mixer", nil)
	reqOp.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tokenOp})
	rrOp := httptest.NewRecorder()
	handleAPISyncMixer(rrOp, reqOp)
	if rrOp.Code != http.StatusForbidden {
		t.Errorf("Esperava 403 Forbidden para OPERATOR em /api/state/sync-mixer, obteve %d", rrOp.Code)
	}

	// 2. Usuário Básico não pode fazer sincronização geral -> 403 Forbidden
	reqBasic := httptest.NewRequest(http.MethodPost, "/api/state/sync-mixer", nil)
	reqBasic.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tokenBasic})
	rrBasic := httptest.NewRecorder()
	handleAPISyncMixer(rrBasic, reqBasic)
	if rrBasic.Code != http.StatusForbidden {
		t.Errorf("Esperava 403 Forbidden para BASIC em /api/state/sync-mixer, obteve %d", rrBasic.Code)
	}

	// 3. Admin pode fazer sincronização geral -> 200 OK
	reqAdmin := httptest.NewRequest(http.MethodPost, "/api/state/sync-mixer", nil)
	reqAdmin.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tokenAdmin})
	rrAdmin := httptest.NewRecorder()
	handleAPISyncMixer(rrAdmin, reqAdmin)
	if rrAdmin.Code != http.StatusOK {
		t.Errorf("Esperava 200 OK para ADMIN em /api/state/sync-mixer, obteve %d", rrAdmin.Code)
	}
}

func TestBasicUserLimitsAndChannelsParsing(t *testing.T) {
	// 1. Testa parseChannelsList
	allChs := parseChannelsList("")
	if len(allChs) != 32 {
		t.Fatalf("Esperava 32 canais para string vazia, obteve %d", len(allChs))
	}

	rangeChs := parseChannelsList("1-4,9,11-12")
	expected := []int{1, 2, 3, 4, 9, 11, 12}
	if len(rangeChs) != len(expected) {
		t.Fatalf("Esperava %d canais, obteve %d (%v)", len(expected), len(rangeChs), rangeChs)
	}
	for i, v := range expected {
		if rangeChs[i] != v {
			t.Errorf("Esperava canal %d na pos %d, obteve %d", v, i, rangeChs[i])
		}
	}

	// Testa parseChannelsList com ponto-e-vírgula ';' e dois pontos ':'
	semiChs := parseChannelsList("1-4;9;11-12")
	if len(semiChs) != len(expected) {
		t.Fatalf("Esperava %d canais com ponto-e-vírgula, obteve %d (%v)", len(expected), len(semiChs), semiChs)
	}
	colonChs := parseChannelsList("1-4:9:11-12")
	if len(colonChs) != len(expected) {
		t.Fatalf("Esperava %d canais com dois pontos, obteve %d (%v)", len(expected), len(colonChs), colonChs)
	}

	// 2. Testa parseOutputsList
	allOuts := parseOutputsList("")
	if len(allOuts) != 9 {
		t.Fatalf("Esperava 9 saídas para string vazia, obteve %d", len(allOuts))
	}

	rangeOuts := parseOutputsList("MASTER,AUX1-AUX3")
	if len(rangeOuts) != 4 {
		t.Fatalf("Esperava 4 saídas, obteve %d (%v)", len(rangeOuts), rangeOuts)
	}
	semiOuts := parseOutputsList("MASTER;AUX1-AUX3")
	if len(semiOuts) != 4 {
		t.Fatalf("Esperava 4 saídas com ponto-e-vírgula, obteve %d (%v)", len(semiOuts), semiOuts)
	}
	colonOuts := parseOutputsList("MASTER:AUX1:AUX2")
	if len(colonOuts) != 3 {
		t.Fatalf("Esperava 3 saídas com dois pontos, obteve %d (%v)", len(colonOuts), colonOuts)
	}

	// 3. Validação do User BASIC
	uBasic := User{
		Username:        "operador_vol",
		Role:            "BASIC",
		AllowedChannels: "1-4",
		AllowedOutputs:  "MASTER;AUX1",
	}

	if !uBasic.IsChannelAllowed(1) || !uBasic.IsChannelAllowed(4) {
		t.Errorf("Deveria permitir canais 1 e 4")
	}
	if uBasic.IsChannelAllowed(5) || uBasic.IsChannelAllowed(32) {
		t.Errorf("Não deveria permitir canais 5 ou 32")
	}

	if !uBasic.IsOutputAllowed("MASTER") || !uBasic.IsOutputAllowed("AUX1") {
		t.Errorf("Deveria permitir MASTER e AUX1")
	}
	if uBasic.IsOutputAllowed("AUX2") || uBasic.IsOutputAllowed("AUX8") {
		t.Errorf("Não deveria permitir AUX2 ou AUX8")
	}

	// 4. Validação de canais avulsos não sequenciais (intercalados)
	sparseChs := parseChannelsList("1;4;7;10;15;22;31")
	expectedSparse := []int{1, 4, 7, 10, 15, 22, 31}
	if len(sparseChs) != len(expectedSparse) {
		t.Fatalf("Esperava %d canais intercalados, obteve %d (%v)", len(expectedSparse), len(sparseChs), sparseChs)
	}
	for i, v := range expectedSparse {
		if sparseChs[i] != v {
			t.Errorf("Esperava canal intercalado %d, obteve %d", v, sparseChs[i])
		}
	}

	uSparse := User{
		Username:        "operador_sparse",
		Role:            "BASIC",
		AllowedChannels: "1;4;7;10;15;22;31",
		AllowedOutputs:  "MASTER;AUX2;AUX5;AUX8",
	}
	for _, ch := range expectedSparse {
		if !uSparse.IsChannelAllowed(ch) {
			t.Errorf("Deveria permitir canal avulso %d", ch)
		}
	}
	for _, ch := range []int{2, 3, 5, 6, 8, 9, 11, 14, 16, 21, 32} {
		if uSparse.IsChannelAllowed(ch) {
			t.Errorf("Não deveria permitir canal não listado %d", ch)
		}
	}
	if !uSparse.IsOutputAllowed("AUX2") || !uSparse.IsOutputAllowed("AUX5") || !uSparse.IsOutputAllowed("AUX8") {
		t.Errorf("Deveria permitir saídas avulsas AUX2, AUX5, AUX8")
	}
	if uSparse.IsOutputAllowed("AUX1") || uSparse.IsOutputAllowed("AUX3") || uSparse.IsOutputAllowed("AUX7") {
		t.Errorf("Não deveria permitir saídas não listadas AUX1, AUX3, AUX7")
	}

	// 5. Usuário ADMIN ou OPERATOR tem acesso irrestrito
	uAdmin := User{Role: "ADMIN", AllowedChannels: "1-2"}
	if !uAdmin.IsChannelAllowed(32) || !uAdmin.IsOutputAllowed("AUX8") {
		t.Errorf("ADMIN deve ter acesso irrestrito independentemente de string")
	}
}

func TestAPIUsersListAndUpdatePermissions(t *testing.T) {
	testFile := "test_api_users.csv"
	defer os.Remove(testFile)
	origCSV := getUsersCSVFile()
	setUsersCSVFile(testFile)
	defer setUsersCSVFile(origCSV)

	content := `nome,usuario,senha,papel,mix,canais,saidas
Admin,admin,admin123,ADMIN,,,
Basico,basico,basico123,BASIC,,1-8,MASTER
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Erro ao criar CSV: %v", err)
	}

	adminToken := createSession(User{Username: "admin", Role: "ADMIN"})
	basicToken := createSession(User{Username: "basico", Role: "BASIC"})

	// 1. Não-admin tentando acessar GET /api/users -> 403 Forbidden
	reqAnon := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	reqAnon.AddCookie(&http.Cookie{Name: sessionCookieName, Value: basicToken})
	rrAnon := httptest.NewRecorder()
	handleAPIUsers(rrAnon, reqAnon)
	if rrAnon.Code != http.StatusForbidden {
		t.Errorf("Esperava 403 para BASIC em /api/users, obteve %d", rrAnon.Code)
	}

	// 2. Admin acessando GET /api/users -> 200 OK com lista
	reqAdmin := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	reqAdmin.AddCookie(&http.Cookie{Name: sessionCookieName, Value: adminToken})
	rrAdmin := httptest.NewRecorder()
	handleAPIUsers(rrAdmin, reqAdmin)
	if rrAdmin.Code != http.StatusOK {
		t.Fatalf("Esperava 200 OK para ADMIN em /api/users, obteve %d", rrAdmin.Code)
	}

	var listResp struct {
		Success bool `json:"success"`
		Users   []struct {
			Username        string `json:"username"`
			Role            string `json:"role"`
			AllowedChannels string `json:"allowed_channels"`
			AllowedOutputs  string `json:"allowed_outputs"`
		} `json:"users"`
	}
	if err := json.Unmarshal(rrAdmin.Body.Bytes(), &listResp); err != nil || len(listResp.Users) != 2 {
		t.Fatalf("Falha ao decodificar usuários: %v (len: %d)", err, len(listResp.Users))
	}

	// 3. Atualizar permissões do usuário basico via POST /api/users
	updatePayload := bytes.NewBufferString(`{
		"username": "basico",
		"allowed_channels": "1-16",
		"allowed_outputs": "MASTER,AUX1,AUX2"
	}`)
	reqPost := httptest.NewRequest(http.MethodPost, "/api/users", updatePayload)
	reqPost.AddCookie(&http.Cookie{Name: sessionCookieName, Value: adminToken})
	rrPost := httptest.NewRecorder()
	handleAPIUsers(rrPost, reqPost)
	if rrPost.Code != http.StatusOK {
		t.Fatalf("Esperava 200 OK ao atualizar usuário, obteve %d (body: %s)", rrPost.Code, rrPost.Body.String())
	}

	// 4. Recarrega os usuários do CSV e confere se gravou
	users, err := loadUsers()
	if err != nil {
		t.Fatalf("loadUsers falhou: %v", err)
	}
	var updatedBasico *User
	for _, u := range users {
		if u.Username == "basico" {
			updatedBasico = &u
			break
		}
	}
	if updatedBasico == nil {
		t.Fatalf("Usuário basico não encontrado após update")
	}
	if updatedBasico.AllowedChannels != "1-16" || updatedBasico.AllowedOutputs != "MASTER;AUX1;AUX2" {
		t.Errorf("Esperava allowed_channels='1-16' e outputs='MASTER;AUX1;AUX2', obteve: channels=%s, outputs=%s",
			updatedBasico.AllowedChannels, updatedBasico.AllowedOutputs)
	}
	if !updatedBasico.IsChannelAllowed(16) || updatedBasico.IsChannelAllowed(17) {
		t.Errorf("Canal 16 deveria ser permitido e 17 bloqueado")
	}
}
