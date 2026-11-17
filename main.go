package main

import (
	"bufio"
	crand "crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/drivers"
	_ "gitlab.com/gomidi/midi/v2/drivers/rtmididrv"
)

type WSMessage struct {
	Type    string `json:"type,omitempty"`    // "init", "param", "simulation_status"
	Port    string `json:"port,omitempty"`    // Nome da porta MIDI
	Hex     string `json:"hex,omitempty"`     // SysEx em hexadecimal
	Element byte   `json:"element"`           // 0x1C (Fader), 0x1A (Mute/ON)
	Param   byte   `json:"param"`             // 0x00
	Channel byte   `json:"channel"`           // 0x00 a 0x1F (0 a 31)
	Value   int    `json:"value"`             // 0 a 1023
	Running *bool  `json:"running,omitempty"` // status da simulação
	Action  string `json:"action,omitempty"`  // "toggle_sim"
}

var (
	midiOut      drivers.Out
	midiPortName string
	midiLock     sync.Mutex
	upgrader     = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	// Gerenciamento de conexões WebSocket para Broadcast em tempo real
	clients     = make(map[*websocket.Conn]bool)
	clientsLock sync.Mutex

	// Controle da simulação de faders
	simRunning  bool
	simLock     sync.Mutex
	simStopChan chan struct{}

	// Throttler global compartilhado entre o simulador e as requisições WebSocket
	throttler = newParamThrottler(20*time.Millisecond, 16*time.Millisecond)
)

// paramKey identifica unicamente um parâmetro da mesa: (element, param, channel)
type paramKey struct {
	element, param, channel byte
}

// paramEntry armazena o último instante de envio e o último valor enviado
type paramEntry struct {
	lastMidi time.Time
	lastWS   time.Time
	lastVal  int
}

// paramThrottler limita a taxa de envio por parâmetro, de forma thread-safe.
// midiInterval: intervalo mínimo entre envios MIDI para o mesmo parâmetro.
// wsInterval:   intervalo mínimo entre broadcasts WebSocket para o mesmo parâmetro.
type paramThrottler struct {
	midiInterval time.Duration
	wsInterval   time.Duration
	mu           sync.Mutex
	entries      map[paramKey]*paramEntry
}

func newParamThrottler(midiInterval, wsInterval time.Duration) *paramThrottler {
	return &paramThrottler{
		midiInterval: midiInterval,
		wsInterval:   wsInterval,
		entries:      make(map[paramKey]*paramEntry),
	}
}

// allow retorna (canSendMIDI, canBroadcastWS) para o parâmetro dado e valor atual.
// Retorna false se o valor não mudou ou se o intervalo mínimo ainda não passou.
func (t *paramThrottler) allow(element, param, channel byte, value int) (midi bool, ws bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := paramKey{element, param, channel}
	now := time.Now()
	entry, exists := t.entries[key]
	if !exists {
		entry = &paramEntry{}
		t.entries[key] = entry
	}

	// Sem mudança de valor — descarta ambos
	if exists && entry.lastVal == value {
		return false, false
	}

	midi = now.Sub(entry.lastMidi) >= t.midiInterval
	ws = now.Sub(entry.lastWS) >= t.wsInterval

	if midi {
		entry.lastMidi = now
	}
	if ws {
		entry.lastWS = now
	}
	if midi || ws {
		entry.lastVal = value
	}
	return
}

func broadcastWS(msg WSMessage) {
	clientsLock.Lock()
	defer clientsLock.Unlock()

	for conn := range clients {
		if err := conn.WriteJSON(msg); err != nil {
			conn.Close()
			delete(clients, conn)
		}
	}
}

// Build01V96SysEx monta a mensagem de 14 bytes da Yamaha 01V96V2
func Build01V96SysEx(element, param, channel byte, value int) []byte {
	if value < 0 {
		value = 0
	}
	if value > 1023 {
		value = 1023
	}

	dd1 := byte(0x00)
	dd2 := byte(0x00)
	dd3 := byte((value >> 7) & 0x7F)
	dd4 := byte(value & 0x7F)

	return []byte{
		0xF0, 0x43, 0x10, 0x3E, 0x0D, 0x01,
		element, param, channel,
		dd1, dd2, dd3, dd4,
		0xF7,
	}
}

// ==========================================
// AUTENTICAÇÃO E GERENCIAMENTO DE USUÁRIOS
// ==========================================

const sessionCookieName = "remote_session"
const sessionDuration = 24 * time.Hour

var (
	usersCSVFile = "usuarios.csv"
	usersLock    sync.RWMutex
)

type User struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"-"`
	Role     string `json:"role"`
}

type Session struct {
	Token     string    `json:"token"`
	User      User      `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

var (
	sessions             = make(map[string]*Session)
	sessionsLock         sync.RWMutex
	sessionsFile         = "sessions.json"
	sessionsFilePathLock sync.RWMutex
)

func setSessionsFile(path string) {
	sessionsFilePathLock.Lock()
	defer sessionsFilePathLock.Unlock()
	if strings.TrimSpace(path) != "" {
		sessionsFile = strings.TrimSpace(path)
	}
}

func getSessionsFile() string {
	sessionsFilePathLock.RLock()
	defer sessionsFilePathLock.RUnlock()
	return sessionsFile
}

// saveSessionsLocked persiste o mapa de sessões em arquivo JSON de forma atômica.
// Deve ser chamado com sessionsLock adquirido (Lock).
func saveSessionsLocked() {
	filePath := getSessionsFile()
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		log.Printf("Aviso: Falha ao serializar sessões: %v", err)
		return
	}

	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		log.Printf("Aviso: Falha ao salvar sessões em %s: %v", tmpFile, err)
		return
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		log.Printf("Aviso: Falha ao atualizar %s: %v", filePath, err)
	}
}

// loadSessions restaura as sessões persistidas do disco descartando as expiradas.
func loadSessions() {
	filePath := getSessionsFile()
	sessionsLock.Lock()
	defer sessionsLock.Unlock()

	data, err := os.ReadFile(filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Aviso: Falha ao ler %s: %v", filePath, err)
		}
		return
	}

	var loaded map[string]*Session
	if err := json.Unmarshal(data, &loaded); err != nil {
		log.Printf("Aviso: Falha ao decodificar %s: %v", filePath, err)
		return
	}

	now := time.Now()
	activeCount := 0
	sessions = make(map[string]*Session)
	for token, sess := range loaded {
		if sess != nil && now.Before(sess.ExpiresAt) {
			sessions[token] = sess
			activeCount++
		}
	}

	log.Printf("Sessões persistidas: %d sessão(ões) ativa(s) carregada(s) de '%s'", activeCount, filePath)
	if len(loaded) != activeCount {
		saveSessionsLocked()
	}
}

func setUsersCSVFile(path string) {
	usersLock.Lock()
	defer usersLock.Unlock()
	if strings.TrimSpace(path) != "" {
		usersCSVFile = strings.TrimSpace(path)
	}
}

func getUsersCSVFile() string {
	usersLock.RLock()
	defer usersLock.RUnlock()
	return usersCSVFile
}

// ensureUsersCSV garante que o arquivo de usuários exista; se não existir, cria um novo com usuário padrão.
func ensureUsersCSV() {
	filePath := getUsersCSVFile()
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		defaultContent := "nome,usuario,senha,papel\nAdministrador,admin,admin,ADMIN\n"
		if err := os.WriteFile(filePath, []byte(defaultContent), 0644); err != nil {
			log.Printf("Aviso: Falha ao inicializar %s: %v", filePath, err)
		} else {
			log.Printf("Arquivo '%s' criado com sucesso com usuário padrão (admin / admin / papel: ADMIN)", filePath)
		}
	}
}

// loadUsers lê o arquivo CSV de usuários diretamente a cada verificação para refletir alterações sem reiniciar o servidor.
func loadUsers() ([]User, error) {
	filePath := getUsersCSVFile()
	ensureUsersCSV()

	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("não foi possível abrir %s: %w", filePath, err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("erro ao processar %s: %w", filePath, err)
	}

	var users []User
	for i, record := range records {
		if len(record) < 4 {
			continue
		}
		name := strings.TrimSpace(record[0])
		username := strings.TrimSpace(record[1])
		password := strings.TrimSpace(record[2])
		role := strings.TrimSpace(record[3])

		// Ignora linha de cabeçalho caso exista
		if i == 0 {
			uLower := strings.ToLower(username)
			rLower := strings.ToLower(role)
			if (uLower == "usuario" || uLower == "usuário" || uLower == "username" || uLower == "user") &&
				(rLower == "papel" || rLower == "role" || rLower == "permissao" || rLower == "permissão") {
				continue
			}
		}

		if username == "" || password == "" {
			continue
		}

		if role == "" {
			role = "ADMIN"
		}

		users = append(users, User{
			Name:     name,
			Username: username,
			Password: password,
			Role:     role,
		})
	}

	return users, nil
}

// authenticateUser valida o usuário e a senha contra o users.csv
func authenticateUser(username, password string) (*User, error) {
	users, err := loadUsers()
	if err != nil {
		return nil, err
	}

	username = strings.TrimSpace(username)
	for _, u := range users {
		if strings.EqualFold(u.Username, username) && u.Password == password {
			return &u, nil
		}
	}
	return nil, fmt.Errorf("usuário ou senha incorretos")
}

func createSession(user User) string {
	b := make([]byte, 32)
	if _, err := crand.Read(b); err != nil {
		return fmt.Sprintf("%d-%s", time.Now().UnixNano(), user.Username)
	}
	token := hex.EncodeToString(b)

	sessionsLock.Lock()
	sessions[token] = &Session{
		Token:     token,
		User:      user,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(sessionDuration),
	}
	saveSessionsLocked()
	sessionsLock.Unlock()

	return token
}

func getSession(token string) *Session {
	if token == "" {
		return nil
	}
	sessionsLock.RLock()
	sess, exists := sessions[token]
	sessionsLock.RUnlock()

	if !exists {
		return nil
	}

	if time.Now().After(sess.ExpiresAt) {
		sessionsLock.Lock()
		delete(sessions, token)
		saveSessionsLocked()
		sessionsLock.Unlock()
		return nil
	}

	return sess
}

func deleteSession(token string) {
	if token == "" {
		return
	}
	sessionsLock.Lock()
	delete(sessions, token)
	saveSessionsLocked()
	sessionsLock.Unlock()
}

func getSessionFromRequest(r *http.Request) *Session {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	return getSession(cookie.Value)
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// isMobileUserAgent verifica se a requisição provém de um dispositivo móvel
func isMobileUserAgent(r *http.Request) bool {
	if r.URL.Query().Get("view") == "mobile" || r.URL.Query().Get("m") == "1" || r.FormValue("view") == "mobile" {
		return true
	}
	if r.URL.Query().Get("view") == "desktop" {
		return false
	}
	if r.Header.Get("Sec-CH-UA-Mobile") == "?1" {
		return true
	}
	ua := strings.ToLower(r.UserAgent())
	mobileKeywords := []string{"android", "iphone", "ipod", "ipad", "blackberry", "iemobile", "opera mini", "mobile"}
	for _, kw := range mobileKeywords {
		if strings.Contains(ua, kw) {
			return true
		}
	}
	return false
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	target := "/"
	if isMobileUserAgent(r) {
		target = "/m"
	}

	if r.Method == http.MethodGet {
		if sess := getSessionFromRequest(r); sess != nil {
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		http.ServeFile(w, r, "login.html")
		return
	}

	if r.Method == http.MethodPost {
		var username, password string
		contentType := r.Header.Get("Content-Type")
		isJSON := strings.Contains(contentType, "application/json")

		if isJSON {
			var req LoginRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"success": false,
					"error":   "Requisição inválida",
				})
				return
			}
			username = req.Username
			password = req.Password
		} else {
			if err := r.ParseForm(); err != nil {
				http.Redirect(w, r, "/login?error=invalid", http.StatusSeeOther)
				return
			}
			username = r.FormValue("username")
			password = r.FormValue("password")
		}

		user, err := authenticateUser(username, password)
		if err != nil {
			log.Printf("Tentativa de login falhou para '%s': %v", username, err)
			if isJSON {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"success": false,
					"error":   "Usuário ou senha incorretos",
				})
				return
			}
			http.Redirect(w, r, "/login?error=invalid", http.StatusSeeOther)
			return
		}

		token := createSession(*user)
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(sessionDuration.Seconds()),
		})

		log.Printf("Usuário autenticado com sucesso: %s (%s, Papel: %s)", user.Name, user.Username, user.Role)

		if isJSON {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success":  true,
				"redirect": target,
				"user": map[string]string{
					"name":     user.Name,
					"username": user.Username,
					"role":     user.Role,
				},
			})
			return
		}

		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	http.Error(w, "Método não permitido", http.StatusMethodNotAllowed)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		deleteSession(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func handleMe(w http.ResponseWriter, r *http.Request) {
	sess := getSessionFromRequest(r)
	if sess == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"authenticated": false,
			"error":         "Não autenticado",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"authenticated": true,
		"name":          sess.User.Name,
		"username":      sess.User.Username,
		"role":          sess.User.Role,
	})
}

func handleMobile(w http.ResponseWriter, r *http.Request) {
	sess := getSessionFromRequest(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.ServeFile(w, r, "mobile.html")
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	sess := getSessionFromRequest(r)
	if sess == nil {
		log.Printf("Tentativa de conexão WebSocket não autorizada de %s rejeitada", r.RemoteAddr)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Erro no upgrade WebSocket: %v", err)
		return
	}
	defer func() {
		clientsLock.Lock()
		delete(clients, conn)
		clientsLock.Unlock()
		conn.Close()
	}()

	clientsLock.Lock()
	clients[conn] = true
	clientsLock.Unlock()

	log.Printf("Cliente WebSocket conectado: %s (Usuário: %s)", r.RemoteAddr, sess.User.Username)

	midiLock.Lock()
	pName := midiPortName
	midiLock.Unlock()

	simLock.Lock()
	runningCopy := simRunning
	simLock.Unlock()

	// Envia informação inicial (porta e status da simulação)
	conn.WriteJSON(WSMessage{
		Type:    "init",
		Port:    pName,
		Running: &runningCopy,
		Hex:     "Conectado ao console Yamaha 01V96V2",
	})

	for {
		var msg WSMessage
		if err := conn.ReadJSON(&msg); err != nil {
			log.Println("Cliente WebSocket desconectado:", err)
			break
		}

		// Ação de alternar simulação solicitada pela tela
		if msg.Action == "toggle_sim" {
			toggleSimulation()
			continue
		}

		// Comando de alteração de fader ou mute vindo da tela
		ApplyParamToState(msg.Element, msg.Param, msg.Channel, msg.Value)
		sysex := Build01V96SysEx(msg.Element, msg.Param, msg.Channel, msg.Value)
		hexStr := fmt.Sprintf("% X", sysex)

		// Log detalhado no terminal identificando a saída / destino e parâmetro
		var targetDesc string
		chNum := msg.Channel + 1
		eqParamNames := map[byte]string{
			0x00: "EQ ON/OFF",
			0x01: "LOW Q/Type",
			0x02: "LOW Freq",
			0x03: "LOW Gain",
			0x04: "LO-MID Q",
			0x05: "LO-MID Freq",
			0x06: "LO-MID Gain",
			0x07: "HI-MID Q",
			0x08: "HI-MID Freq",
			0x09: "HI-MID Gain",
			0x0A: "HIGH Q/Type",
			0x0B: "HIGH Freq",
			0x0C: "HIGH Gain",
			0x0D: "HPF ON/OFF",
			0x0E: "LPF ON/OFF",
		}
		compParamNames := map[byte]string{
			0x00: "COMP ON/OFF",
			0x01: "Threshold",
			0x02: "Ratio",
			0x03: "Attack",
			0x04: "Release",
			0x05: "Knee",
			0x06: "Gain (Out Gain)",
		}

		switch msg.Element {
		case 0x1C:
			targetDesc = fmt.Sprintf("CH %02d -> MASTER Fader: %d (%s)", chNum, msg.Value, faderValueToDB(msg.Value))
		case 0x1A:
			onStr := "MUTE (0)"
			if msg.Value == 1 {
				onStr = "ON (1)"
			}
			targetDesc = fmt.Sprintf("CH %02d -> MASTER Channel: %s", chNum, onStr)
		case 0x23:
			if msg.Param <= 0x07 {
				auxNum := msg.Param + 1
				targetDesc = fmt.Sprintf("CH %02d -> AUX %d Send Level: %d (%s)", chNum, auxNum, msg.Value, faderValueToDB(msg.Value))
			} else if msg.Param >= 0x08 && msg.Param <= 0x0F {
				auxNum := (msg.Param - 0x08) + 1
				onStr := "MUTE (0)"
				if msg.Value == 1 {
					onStr = "ON (1)"
				}
				targetDesc = fmt.Sprintf("CH %02d -> AUX %d Send: %s", chNum, auxNum, onStr)
			} else {
				targetDesc = fmt.Sprintf("CH %02d -> Element 0x%02X, Param 0x%02X: %d", chNum, msg.Element, msg.Param, msg.Value)
			}
		case 0x20:
			pName, ok := eqParamNames[msg.Param]
			if !ok {
				pName = fmt.Sprintf("Param 0x%02X", msg.Param)
			}
			targetDesc = fmt.Sprintf("CH %02d -> EQ %s: %d", chNum, pName, msg.Value)
		case 0x22:
			pName, ok := compParamNames[msg.Param]
			if !ok {
				pName = fmt.Sprintf("Param 0x%02X", msg.Param)
			}
			targetDesc = fmt.Sprintf("CH %02d -> COMP %s: %d", chNum, pName, msg.Value)
		case 0x4D:
			targetDesc = fmt.Sprintf("STEREO MASTER Out Fader: %d (%s)", msg.Value, faderValueToDB(msg.Value))
		case 0x4B:
			onStr := "MUTE (0)"
			if msg.Value == 1 {
				onStr = "ON (1)"
			}
			targetDesc = fmt.Sprintf("STEREO MASTER Out: %s", onStr)
		case 0x31:
			auxNum := msg.Channel + 1
			targetDesc = fmt.Sprintf("AUX %d MASTER Fader: %d (%s)", auxNum, msg.Value, faderValueToDB(msg.Value))
		case 0x2F:
			auxNum := msg.Channel + 1
			onStr := "MUTE (0)"
			if msg.Value == 1 {
				onStr = "ON (1)"
			}
			targetDesc = fmt.Sprintf("AUX %d MASTER Out: %s", auxNum, onStr)
		case 0x35:
			auxNum := msg.Channel + 1
			pName, ok := eqParamNames[msg.Param]
			if !ok {
				pName = fmt.Sprintf("Param 0x%02X", msg.Param)
			}
			targetDesc = fmt.Sprintf("AUX %d MASTER Out -> EQ %s: %d", auxNum, pName, msg.Value)
		case 0x36:
			auxNum := msg.Channel + 1
			pName, ok := compParamNames[msg.Param]
			if !ok {
				pName = fmt.Sprintf("Param 0x%02X", msg.Param)
			}
			targetDesc = fmt.Sprintf("AUX %d MASTER Out -> COMP %s: %d", auxNum, pName, msg.Value)
		case 0x50:
			pName, ok := eqParamNames[msg.Param]
			if !ok {
				pName = fmt.Sprintf("Param 0x%02X", msg.Param)
			}
			targetDesc = fmt.Sprintf("STEREO MASTER Out -> EQ %s: %d", pName, msg.Value)
		case 0x51:
			pName, ok := compParamNames[msg.Param]
			if !ok {
				pName = fmt.Sprintf("Param 0x%02X", msg.Param)
			}
			targetDesc = fmt.Sprintf("STEREO MASTER Out -> COMP %s: %d", pName, msg.Value)
		default:
			targetDesc = fmt.Sprintf("CH %02d -> Element 0x%02X, Param 0x%02X: %d", chNum, msg.Element, msg.Param, msg.Value)
		}

		fmt.Printf("WS -> SysEx: %s | %s\n", hexStr, targetDesc)

		// Aplica throttle antes de enviar ao barramento MIDI e ao WebSocket.
		// Eventos ON/MUTE/Bypass discretos passam direto sem throttling
		isDiscrete := msg.Element == 0x1A ||
			msg.Element == 0x4B || msg.Element == 0x2F ||
			(msg.Element == 0x23 && msg.Param >= 0x08) ||
			((msg.Element == 0x20 || msg.Element == 0x35 || msg.Element == 0x50) && (msg.Param == 0x00 || msg.Param == 0x0D || msg.Param == 0x0E)) ||
			((msg.Element == 0x22 || msg.Element == 0x36 || msg.Element == 0x51) && msg.Param == 0x00)
		canMIDI, canWS := true, true
		if !isDiscrete {
			canMIDI, canWS = throttler.allow(msg.Element, msg.Param, msg.Channel, msg.Value)
		}

		// Envia para a saída MIDI física
		if canMIDI {
			midiLock.Lock()
			if midiOut != nil {
				if err := midiOut.Send(sysex); err != nil {
					log.Printf("Erro ao enviar MIDI: %v", err)
				}
			}
			midiLock.Unlock()
		}

		// Broadcast para manter sincronizada qualquer outra aba aberta
		if canWS {
			broadcastWS(WSMessage{
				Type:    "param",
				Element: msg.Element,
				Param:   msg.Param,
				Channel: msg.Channel,
				Value:   msg.Value,
				Hex:     hexStr,
			})
		}
	}
}

// selectMIDIPort gerencia a escolha da porta MIDI (via flag ou interativamente no terminal antes de iniciar o serviço)
func selectMIDIPort(outs []drivers.Out, flagPort string) drivers.Out {
	// Se foi especificado por flag de linha de comando
	if flagPort != "" {
		trimmed := strings.TrimSpace(flagPort)
		if strings.EqualFold(trimmed, "s") || strings.EqualFold(trimmed, "sim") || strings.EqualFold(trimmed, "none") {
			log.Println("Modo de simulação selecionado via parâmetro de linha de comando.")
			return nil
		}

		// Tenta interpretar como índice numérico
		if idx, err := strconv.Atoi(trimmed); err == nil {
			if idx >= 0 && idx < len(outs) {
				out := outs[idx]
				if err := out.Open(); err != nil {
					log.Printf("Erro ao abrir porta %d (%s): %v. Prosseguindo para seleção interativa.", idx, out.String(), err)
				} else {
					return out
				}
			} else {
				log.Printf("Índice de porta %d inválido (disponíveis: 0 a %d).", idx, len(outs)-1)
			}
		} else {
			// Busca por substring no nome da porta
			for _, out := range outs {
				if strings.Contains(strings.ToLower(out.String()), strings.ToLower(trimmed)) {
					if err := out.Open(); err != nil {
						log.Printf("Erro ao abrir porta %s: %v. Prosseguindo para seleção interativa.", out.String(), err)
						break
					}
					return out
				}
			}
			log.Printf("Nenhuma porta encontrada contendo o termo '%s'.", trimmed)
		}
	}

	// Caso não haja portas MIDI disponíveis no sistema
	if len(outs) == 0 {
		fmt.Println("\n⚠️  Nenhuma porta MIDI de saída foi encontrada no sistema.")
		fmt.Println("O serviço funcionará em modo de simulação (apenas gerando SysEx nos logs e na tela).")
		return nil
	}

	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Println("\n==============================================================")
		fmt.Println("  Yamaha 01V96 Controller — Seleção de Porta MIDI de Saída")
		fmt.Println("==============================================================")
		fmt.Println("Portas MIDI disponíveis:")
		for i, out := range outs {
			defaultMark := ""
			if i == 0 {
				defaultMark = " (Padrão)"
			}
			fmt.Printf("  [%d] %s%s\n", i, out.String(), defaultMark)
		}
		fmt.Println("  [s] Modo Simulação (apenas log, sem envio físico)")
		fmt.Println("  [q] Sair")
		fmt.Println("--------------------------------------------------------------")
		fmt.Print("Escolha a porta desejada [0]: ")

		input, err := reader.ReadString('\n')
		if err != nil {
			// EOF atingido (ex: execução não-interativa ou pipe)
			fmt.Println("\nEntrada não-interativa detectada. Utilizando porta padrão [0].")
			out := outs[0]
			if err := out.Open(); err != nil {
				log.Printf("Aviso: não foi possível abrir %s: %v", out.String(), err)
				return nil
			}
			return out
		}

		input = strings.TrimSpace(input)

		// Opção padrão [0] ao pressionar Enter direto
		if input == "" {
			input = "0"
		}

		if strings.EqualFold(input, "q") {
			fmt.Println("Encerrando aplicação.")
			os.Exit(0)
		}

		if strings.EqualFold(input, "s") {
			fmt.Println("Modo Simulação ativado.")
			return nil
		}

		idx, err := strconv.Atoi(input)
		if err != nil || idx < 0 || idx >= len(outs) {
			fmt.Printf("\n❌ Opção inválida '%s'. Por favor, digite um número entre 0 e %d, 's' ou 'q'.\n", input, len(outs)-1)
			continue
		}

		out := outs[idx]
		if err := out.Open(); err != nil {
			fmt.Printf("\n❌ Não foi possível abrir a porta '%s': %v\n", out.String(), err)
			fmt.Println("Por favor, selecione outra porta ou use o modo simulação [s].")
			continue
		}

		return out
	}
}

// faderChannelState armazena o estado atual e alvo de um fader na simulação
type faderChannelState struct {
	currentVal int
	targetVal  int
	holdTicks  int
}

// faderValueToDB converte o valor decimal de 0 a 1023 para uma representação aproximada em dB
// baseada nos pontos da tabela SysEx da Yamaha 01V96V2
func faderValueToDB(v int) string {
	if v <= 0 {
		return "-∞ dB"
	}
	type calPoint struct {
		val int
		db  float64
	}
	cal := []calPoint{
		{0, -99.9},
		{224, -40.0},
		{464, -20.0},
		{624, -10.0},
		{763, -5.0},
		{823, 0.0},
		{923, 5.0},
		{1023, 10.0},
	}
	if v >= 1023 {
		return "+10.0 dB"
	}
	for i := 0; i < len(cal)-1; i++ {
		if v >= cal[i].val && v <= cal[i+1].val {
			ratio := float64(v-cal[i].val) / float64(cal[i+1].val-cal[i].val)
			db := cal[i].db + ratio*(cal[i+1].db-cal[i].db)
			if db > 0 {
				return fmt.Sprintf("+%.1f dB", db)
			}
			return fmt.Sprintf("%.1f dB", db)
		}
	}
	return fmt.Sprintf("%d", v)
}

func startSimulation(out drivers.Out) {
	simLock.Lock()
	if simRunning {
		simLock.Unlock()
		return
	}
	simRunning = true
	simStopChan = make(chan struct{})
	simLock.Unlock()

	runningTrue := true
	broadcastWS(WSMessage{
		Type:    "simulation_status",
		Running: &runningTrue,
	})
	log.Println("Simulação de faders iniciada (Canais 1 a 8).")

	go runFaderLoop(out, simStopChan)
}

func stopSimulation() {
	simLock.Lock()
	if !simRunning {
		simLock.Unlock()
		return
	}
	simRunning = false
	if simStopChan != nil {
		close(simStopChan)
		simStopChan = nil
	}
	simLock.Unlock()

	runningFalse := false
	broadcastWS(WSMessage{
		Type:    "simulation_status",
		Running: &runningFalse,
	})
	log.Println("Simulação de faders pausada.")
}

func toggleSimulation() {
	simLock.Lock()
	running := simRunning
	simLock.Unlock()

	if running {
		stopSimulation()
	} else {
		startSimulation(midiOut)
	}
}

// mixSimState agrupa os estados de fader dos 8 canais para uma mixagem (MASTER ou AUX N)
type mixSimState struct {
	name     string // "MASTER", "AUX1" … "AUX8"
	element  byte   // 0x1C = Master fader, 0x23 = Aux send
	param    byte   // 0x00 p/ MASTER, 0x00–0x07 p/ AUX 1–8
	channels []faderChannelState
}

// runFaderLoop simula a movimentação contínua e suave de faders em TODAS as 9 mixagens
// (MASTER + AUX 1–8) de 8 canais cada, enviando SysEx + WebSocket em tempo real.
func runFaderLoop(out drivers.Out, stopChan chan struct{}) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	const numChannels = 8

	// Define as 9 mixagens com seus parâmetros SysEx correspondentes
	mixes := []mixSimState{
		{name: "MASTER", element: 0x1C, param: 0x00},
		{name: "AUX1", element: 0x23, param: 0x00},
		{name: "AUX2", element: 0x23, param: 0x01},
		{name: "AUX3", element: 0x23, param: 0x02},
		{name: "AUX4", element: 0x23, param: 0x03},
		{name: "AUX5", element: 0x23, param: 0x04},
		{name: "AUX6", element: 0x23, param: 0x05},
		{name: "AUX7", element: 0x23, param: 0x06},
		{name: "AUX8", element: 0x23, param: 0x07},
	}

	// Inicializa os canais de cada mixagem com posições variadas mas realistas
	for m := range mixes {
		mixes[m].channels = make([]faderChannelState, numChannels)
		for ch := 0; ch < numChannels; ch++ {
			var initVal int
			if m == 0 {
				// MASTER: próximo de 0 dB (650–849)
				initVal = 650 + r.Intn(200)
			} else {
				// AUX: posições de send mais variadas (200–800)
				initVal = 200 + r.Intn(600)
			}
			mixes[m].channels[ch] = faderChannelState{
				currentVal: initVal,
				targetVal:  initVal,
				holdTicks:  r.Intn(20),
			}
		}
	}

	// Ciclo de atualização de 40ms (~25 FPS)
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()

	// Contador de ticks para escalonar as atualizações das mixagens AUX
	// (AUX atualiza com menos frequência para não saturar o WebSocket)
	tick := 0

	for {
		select {
		case <-stopChan:
			return
		case <-ticker.C:
			tick++

			for m := range mixes {
				// MASTER atualiza todo tick; cada AUX atualiza a cada 2 ticks,
				// escalonados para não coincidir todos no mesmo instante
				if m > 0 && (tick%2 != m%2) {
					continue
				}

				mix := &mixes[m]
				for ch := 0; ch < numChannels; ch++ {
					st := &mix.channels[ch]

					// Se o fader está no alvo, aguarda e sorteia próximo movimento
					if st.currentVal == st.targetVal {
						if st.holdTicks > 0 {
							st.holdTicks--
							continue
						}
						// Probabilidade de movimento varia: MASTER 6%, AUX 4%
						threshold := float32(0.06)
						if m > 0 {
							threshold = 0.04
						}
						if r.Float32() < threshold {
							dice := r.Float32()
							var newTarget int
							if dice < 0.05 {
								newTarget = 0 // fade out
							} else if dice > 0.95 {
								newTarget = 900 + r.Intn(124) // boost
							} else {
								delta := r.Intn(300) - 150
								newTarget = st.currentVal + delta
								minVal := 150
								if m == 0 {
									minVal = 250 // MASTER raramente vai muito baixo
								}
								if newTarget < minVal {
									newTarget = minVal + r.Intn(100)
								}
								if newTarget > 950 {
									newTarget = 950
								}
							}
							st.targetVal = newTarget
						}
						continue
					}

					// Movimento suave em direção ao alvo
					diff := st.targetVal - st.currentVal
					step := 5 + r.Intn(8)
					if diff > 0 {
						if diff < step {
							st.currentVal = st.targetVal
						} else {
							st.currentVal += step
						}
					} else {
						if -diff < step {
							st.currentVal = st.targetVal
						} else {
							st.currentVal -= step
						}
					}

					if st.currentVal == st.targetVal {
						st.holdTicks = 10 + r.Intn(60)
					}

					// Monta e envia SysEx com element e param corretos para cada mixagem
					sysex := Build01V96SysEx(mix.element, mix.param, byte(ch), st.currentVal)
					hexStr := fmt.Sprintf("% X", sysex)

					// Throttle: o simulador pode gerar até 72 fluxos paralelos;
					// descartamos envios repetidos abaixo do intervalo mínimo por parâmetro.
					canMIDI, canWS := throttler.allow(mix.element, mix.param, byte(ch), st.currentVal)

					if canMIDI {
						midiLock.Lock()
						if out != nil {
							if err := out.Send(sysex); err != nil {
								log.Printf("Erro ao enviar SysEx MIDI [%s Ch %d]: %v", mix.name, ch+1, err)
							}
						}
						midiLock.Unlock()
					}

					if canWS {
						broadcastWS(WSMessage{
							Type:    "param",
							Element: mix.element,
							Param:   mix.param,
							Channel: byte(ch),
							Value:   st.currentVal,
							Hex:     hexStr,
						})
					}

					if canMIDI || canWS {
						ApplyParamToState(mix.element, mix.param, byte(ch), st.currentVal)
						dbStr := faderValueToDB(st.currentVal)
						sentFlags := ""
						if canMIDI {
							sentFlags += "MIDI "
						}
						if canWS {
							sentFlags += "WS"
						}
						fmt.Printf("[%s Ch %d] %4d (%8s) [%s]| SysEx: %s\n",
							mix.name, ch+1, st.currentVal, dbStr, sentFlags, hexStr)
					}
				}
			}
		}
	}
}

func main() {
	flagPort := flag.String("port", "", "Porta MIDI de saída por índice ou nome (ex: -port 1 ou -port Delta). Deixe vazio para escolher interativamente.")
	flagAddr := flag.String("addr", ":8080", "Endereço HTTP do servidor")
	flagSimulate := flag.Bool("simulate", false, "Inicia o simulador de operação da Yamaha 01V96 emitindo faders nos canais 1 a 8")
	flag.BoolVar(flagSimulate, "sim", false, "Atalho para -simulate")
	flagUsers := flag.String("users", "usuarios.csv", "Caminho do arquivo CSV de credenciais de usuários (padrão: usuarios.csv)")
	flag.StringVar(flagUsers, "users-file", "usuarios.csv", "Atalho para -users")
	flagSessions := flag.String("sessions", "sessions.json", "Caminho do arquivo de persistência de sessões (padrão: sessions.json)")
	flagData := flag.String("data", "data", "Diretório de persistência de cenas e auto-save (padrão: data)")
	flag.Parse()

	// Configura e inicializa o gerenciador de estado, auto-save e cenas
	InitSceneManager(*flagData)

	// Configura e valida o arquivo de credenciais
	setUsersCSVFile(*flagUsers)
	ensureUsersCSV()

	// Configura e restaura as sessões persistidas do disco
	setSessionsFile(*flagSessions)
	loadSessions()

	initialUsers, err := loadUsers()
	if err != nil {
		log.Fatalf("[ERRO FATAL] Falha ao processar arquivo de credenciais '%s': %v", getUsersCSVFile(), err)
	}
	if len(initialUsers) == 0 {
		log.Fatalf("[ERRO FATAL] Nenhum usuário disponível no arquivo '%s'. A aplicação não pode ser iniciada sem usuários cadastrados.", getUsersCSVFile())
	}
	log.Printf("Sistema de autenticação pronto: %d usuário(s) ativo(s) a partir de '%s'", len(initialUsers), getUsersCSVFile())

	defer midi.CloseDriver()

	outs := midi.GetOutPorts()
	midiOut = selectMIDIPort(outs, *flagPort)

	if midiOut != nil {
		defer midiOut.Close()
		midiPortName = midiOut.String()
		log.Printf("Conectado na saída MIDI: %s", midiPortName)
	} else {
		midiPortName = "Modo Simulação (sem porta MIDI física)"
		log.Println("Rodando em modo simulação (nenhuma porta MIDI física conectada).")
	}

	// Se o modo simulação foi solicitado por linha de comando, inicia os faders
	if *flagSimulate {
		startSimulation(midiOut)
	}

	http.HandleFunc("/login", handleLogin)
	http.HandleFunc("/logout", handleLogout)
	http.HandleFunc("/api/me", handleMe)
	http.HandleFunc("/api/scenes", handleAPIScenes)
	http.HandleFunc("/api/scenes/", handleAPISceneDetail)
	http.HandleFunc("/api/scenes/import", handleAPIImportScene)
	http.HandleFunc("/api/state/status", handleAPIStateStatus)
	http.HandleFunc("/api/state/restore-last", handleAPIRestoreLastState)
	http.HandleFunc("/api/state", handleAPIGetCurrentState)
	http.HandleFunc("/m", handleMobile)
	http.HandleFunc("/mobile", handleMobile)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		sess := getSessionFromRequest(r)
		if sess == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		// Se o usuário estiver no celular e não tiver solicitado explicitamente a visão desktop, redireciona para /m
		if isMobileUserAgent(r) && r.URL.Query().Get("view") != "desktop" {
			http.Redirect(w, r, "/m", http.StatusSeeOther)
			return
		}
		http.ServeFile(w, r, "index.html")
	})
	http.HandleFunc("/ws", handleWS)

	// Captura Ctrl+C para desligamento gracioso
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n[!] Encerrando servidor e simulação...")
		stopSimulation()
		StopAutoSave()
		os.Exit(0)
	}()

	log.Printf("Servidor Console Web rodando em http://%s", *flagAddr)
	log.Fatal(http.ListenAndServe(*flagAddr, nil))
}
