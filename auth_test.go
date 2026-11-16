package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
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

func TestSessionLifecycle(t *testing.T) {
	testUser := User{
		Name:     "Test Admin",
		Username: "admin_test",
		Role:     "ADMIN",
	}

	token := createSession(testUser)
	if token == "" {
		t.Fatalf("Token de sessão veio vazio")
	}

	sess := getSession(token)
	if sess == nil {
		t.Fatalf("Sessão não encontrada")
	}
	if sess.User.Username != "admin_test" {
		t.Fatalf("Esperava username 'admin_test', obteve '%s'", sess.User.Username)
	}

	deleteSession(token)
	if getSession(token) != nil {
		t.Fatalf("Sessão ainda existe após exclusão")
	}
}

func TestHTTPLoginAndProtectedEndpoints(t *testing.T) {
	setUsersCSVFile("usuarios.csv")
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
}
