package main

import (
	"bufio"
	"bytes"
	"context"
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
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
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
	Type        string       `json:"type,omitempty"`          // "init", "param", "simulation_status", "mute_groups"
	Port        string       `json:"port,omitempty"`          // Nome da porta MIDI
	Hex         string       `json:"hex,omitempty"`           // SysEx em hexadecimal
	Element     byte         `json:"element"`                 // 0x1C (Fader), 0x1A (Mute/ON)
	Param       byte         `json:"param"`                   // 0x00
	Channel     byte         `json:"channel"`                 // 0x00 a 0x1F (0 a 31)
	Value       int          `json:"value"`                   // 0 a 1023
	Running     *bool        `json:"running,omitempty"`       // status da simulação
	Action      string       `json:"action,omitempty"`        // "toggle_sim", "sync_main", "sync_aux", "sync_fx", "personal_mix_joystick", "toggle_mute_group", "set_mute_group"
	Aux         int          `json:"aux,omitempty"`           // 1 a 8 para sync_aux
	Target      string       `json:"target,omitempty"`        // Identificador do canal/saída para sync_fx
	MixID       string       `json:"mix_id,omitempty"`        // ID da mix pessoal para personal_mix_joystick / personal_mix_master
	JoyX        float64      `json:"x,omitempty"`             // Coordenada X (-1..1) do joystick 3 eixos
	JoyY        float64      `json:"y,omitempty"`             // Coordenada Y (-1..1) do joystick 3 eixos
	MasterLevel float64      `json:"level,omitempty"`         // Nível relativo (0.0..1.0) do Master da mix pessoal
	Persist     bool         `json:"persist,omitempty"`       // Salvar posição do joystick/master em disco
	MuteGroupID int          `json:"mute_group_id,omitempty"` // 1 a 4 para toggle_mute_group / set_mute_group
	MuteGroups  []*MuteGroup `json:"mute_groups,omitempty"`   // Estado completo dos Mute Groups da aplicação
	GRDb        float64      `json:"gr_db,omitempty"`         // Gain Reduction em dB (0.0 .. 24.0)
	Raw         int          `json:"raw,omitempty"`           // Valor bruto 14-bit para diagnóstico/calibração
}

var (
	midiOut           drivers.Out
	midiThroughMirror drivers.Out
	midiRawFiles      []*os.File
	midiPortName      string
	midiLock          sync.Mutex
	upgrader          = websocket.Upgrader{
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

	// Controle de desligamento do servidor/máquina via interface administrativa
	canShutdownServer bool
	httpServer        *http.Server
	shutdownExecFunc  = executeSystemShutdown

	virmidiPortRegex = regexp.MustCompile(`(?i)(?:Virtual Raw MIDI|VirMIDI)\s+(\d+)-(\d+)`)
)

// closeMIDIBridgesLocked fecha arquivos RawMIDI e portas espelho auxiliares (deve ser chamado com midiLock)
func closeMIDIBridgesLocked() {
	for _, rf := range midiRawFiles {
		if rf != nil {
			_ = rf.Close()
		}
	}
	midiRawFiles = nil
	if midiThroughMirror != nil {
		_ = midiThroughMirror.Close()
		midiThroughMirror = nil
	}
}

// setupMIDIBridges configura bridges para portas virtuais ALSA (snd-virmidi e Midi Through).
// No Linux ALSA, eventos enviados via ALSA Sequencer (rtmididrv) para uma porta VirMIDI (ex: 24:0)
// saem apenas no dispositivo RawMIDI (/dev/snd/midiC2D0) e NÃO são entregues a clientes ALSA Sequencer
// inscritos na mesma porta (como Wine / Studio Manager e aseqdump -p 24:0).
// Por outro lado, escrever diretamente em /dev/snd/midiC<card>D<dev> injeta o SysEx no driver snd-virmidi
// para que ele seja emitido na saída ALSA Sequencer da porta VirMIDI.
func setupMIDIBridges(outs []drivers.Out, selected drivers.Out) {
	midiLock.Lock()
	defer midiLock.Unlock()

	closeMIDIBridgesLocked()
	if selected == nil {
		return
	}

	selName := selected.String()
	selLower := strings.ToLower(selName)

	// 1. Se a porta selecionada for VirMIDI (ex: "Virtual Raw MIDI 2-0:VirMIDI 2-0 24:0"),
	// abre o dispositivo RawMIDI correspondente (/dev/snd/midiC2D0) para garantir que clientes
	// ALSA Sequencer (Wine / Studio Manager / aseqdump) recebam os pacotes SysEx na porta 24:0,
	// e também espelha na Midi Through (14:0) caso o Studio Manager esteja ouvindo nela (como no first commit).
	if matches := virmidiPortRegex.FindStringSubmatch(selName); len(matches) == 3 {
		rawPath := fmt.Sprintf("/dev/snd/midiC%sD%s", matches[1], matches[2])
		if rf, err := os.OpenFile(rawPath, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			midiRawFiles = append(midiRawFiles, rf)
			log.Printf("Bridge ALSA VirMIDI ativo: injetando SysEx via %s para entrega no ALSA Sequencer (%s)", rawPath, selName)
		} else {
			log.Printf("Aviso: não foi possível abrir bridge RawMIDI %s: %v", rawPath, err)
		}

		for _, o := range outs {
			if o.String() != selName && strings.Contains(strings.ToLower(o.String()), "midi through") {
				if err := o.Open(); err == nil {
					midiThroughMirror = o
					log.Printf("Bridge Midi Through ativo: espelhando SysEx também em '%s'", o.String())
				}
				break
			}
		}
		return
	}

	// 2. Se a porta selecionada for Midi Through (ex: "Midi Through:Midi Through Port-0 14:0", padrão do first commit),
	// verifica se há portas VirMIDI carregadas no sistema e espelha na primeira VirMIDI (ex: /dev/snd/midiC2D0)
	// para que o Studio Manager responda tanto se estiver configurado em Midi Through quanto em VirMIDI 2-0.
	if strings.Contains(selLower, "midi through") {
		for _, o := range outs {
			if matches := virmidiPortRegex.FindStringSubmatch(o.String()); len(matches) == 3 {
				rawPath := fmt.Sprintf("/dev/snd/midiC%sD%s", matches[1], matches[2])
				if rf, err := os.OpenFile(rawPath, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					midiRawFiles = append(midiRawFiles, rf)
					log.Printf("Bridge ALSA VirMIDI ativo: espelhando SysEx também em %s (%s)", rawPath, o.String())
				}
				break
			}
		}
	}
}

// sendMIDISysExLocked transmite o pacote SysEx para a porta MIDI ativa e eventuais bridges virtuais.
// Deve ser chamado com midiLock adquirido.
func sendMIDISysExLocked(sysex []byte) error {
	var firstErr error
	if midiOut != nil {
		if err := midiOut.Send(sysex); err != nil {
			firstErr = err
		}
	}
	for _, rf := range midiRawFiles {
		if rf != nil {
			_, _ = rf.Write(sysex)
		}
	}
	if midiThroughMirror != nil {
		_ = midiThroughMirror.Send(sysex)
	}
	return firstErr
}

// paramKey identifica unicamente um parâmetro da mesa: (element, param, channel)
type paramKey struct {
	element, param, channel byte
}

// paramEntry armazena o último instante de envio e o último valor enviado separadamente para MIDI e WS
type paramEntry struct {
	lastMidi     time.Time
	lastWS       time.Time
	lastMidiVal  int
	lastWSVal    int
	hasMidiVal   bool
	hasWSVal     bool
	pendingTimer *time.Timer
	pendingVal   int
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

// allow retorna (canSendMIDI, canBroadcastWS) para o parâmetro dado e valor atual (usado pelo simulador).
// Rastreia lastMidiVal e lastWSVal de forma independente para que um broadcast WS nunca bloqueie o envio MIDI.
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

	if (!entry.hasMidiVal || entry.lastMidiVal != value) && now.Sub(entry.lastMidi) >= t.midiInterval {
		midi = true
		entry.lastMidi = now
		entry.lastMidiVal = value
		entry.hasMidiVal = true
	}

	if (!entry.hasWSVal || entry.lastWSVal != value) && now.Sub(entry.lastWS) >= t.wsInterval {
		ws = true
		entry.lastWS = now
		entry.lastWSVal = value
		entry.hasWSVal = true
	}

	return
}

// allowOrSchedule é usado para comandos reais do usuário via WebSocket:
// Se o intervalo mínimo de MIDI já passou, envia imediatamente (mesmo que o valor seja igual ao anterior,
// permitindo re-sincronizar a mesa ao clicar em 0dB ou -inf).
// Se estiver dentro da janela de throttle (< 20ms), agenda um flush automático (trailing edge)
// garantindo que a posição final do fader ou knob NUNCA seja descartada.
func (t *paramThrottler) allowOrSchedule(element, param, channel byte, value int, flushMIDI func(finalVal int)) (midi bool, ws bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := paramKey{element, param, channel}
	now := time.Now()
	entry, exists := t.entries[key]
	if !exists {
		entry = &paramEntry{}
		t.entries[key] = entry
	}

	elapsedMIDI := now.Sub(entry.lastMidi)
	if elapsedMIDI >= t.midiInterval {
		if entry.pendingTimer != nil {
			entry.pendingTimer.Stop()
			entry.pendingTimer = nil
		}
		midi = true
		entry.lastMidi = now
		entry.lastMidiVal = value
		entry.hasMidiVal = true
	} else {
		entry.pendingVal = value
		if entry.pendingTimer == nil && flushMIDI != nil {
			wait := t.midiInterval - elapsedMIDI
			entry.pendingTimer = time.AfterFunc(wait, func() {
				t.mu.Lock()
				e := t.entries[key]
				if e == nil {
					t.mu.Unlock()
					return
				}
				finalVal := e.pendingVal
				e.lastMidi = time.Now()
				e.lastMidiVal = finalVal
				e.hasMidiVal = true
				e.pendingTimer = nil
				t.mu.Unlock()

				markSentEcho(element, param, channel, finalVal)
				flushMIDI(finalVal)
			})
		}
	}

	if now.Sub(entry.lastWS) >= t.wsInterval || midi {
		ws = true
		entry.lastWS = now
		entry.lastWSVal = value
		entry.hasWSVal = true
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

// Build01V96SysEx monta a mensagem de 14 bytes da Yamaha 01V96V2 (Universal Digital Mixer Format)
// Suporta números inteiros com sinal de 28 bits (faders, pan, ganhos e thresholds negativos).
func Build01V96SysEx(element, param, channel byte, value int) []byte {
	u := uint32(value) & 0x0FFFFFFF
	dd1 := byte((u >> 21) & 0x7F)
	dd2 := byte((u >> 14) & 0x7F)
	dd3 := byte((u >> 7) & 0x7F)
	dd4 := byte(u & 0x7F)

	return []byte{
		0xF0, 0x43, 0x10, 0x3E, 0x7F, 0x01,
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
	Name               string `json:"name"`
	Username           string `json:"username"`
	Password           string `json:"-"`
	Role               string `json:"role"`
	MixID              string `json:"mix_id,omitempty"`
	AllowedChannels    string `json:"allowed_channels,omitempty"` // Lista/Faixa (ex: "1-8,12" ou "" para todos)
	AllowedOutputs     string `json:"allowed_outputs,omitempty"`  // Lista (ex: "MASTER,AUX1,AUX2" ou "" para todos)
	MustChangePassword bool   `json:"must_change_password,omitempty"`
}

func parseBoolFlag(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "sim", "yes", "s", "y":
		return true
	default:
		return false
	}
}

var allStandardOutputs = []string{"MASTER", "AUX1", "AUX2", "AUX3", "AUX4", "AUX5", "AUX6", "AUX7", "AUX8"}

// parseChannelsList interpreta strings como "1-8,12,14-16" ou "ALL" ou "*" e retorna os canais de 1 a 32.
func parseChannelsList(raw string) []int {
	raw = strings.TrimSpace(strings.ToUpper(raw))
	if raw == "" || raw == "*" || raw == "ALL" || raw == "TODOS" {
		all := make([]int, 32)
		for i := 0; i < 32; i++ {
			all[i] = i + 1
		}
		return all
	}

	seen := make(map[int]bool)
	var result []int

	// Aceita separadores ';', ',', ':', espaços ou tabulações
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ';' || r == ',' || r == ':' || r == ' ' || r == '\t'
	})
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, "-") {
			rangeParts := strings.Split(p, "-")
			if len(rangeParts) == 2 {
				start, err1 := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
				end, err2 := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
				if err1 == nil && err2 == nil {
					if start > end {
						start, end = end, start
					}
					for ch := start; ch <= end; ch++ {
						if ch >= 1 && ch <= 32 && !seen[ch] {
							seen[ch] = true
							result = append(result, ch)
						}
					}
				}
			}
		} else {
			ch, err := strconv.Atoi(p)
			if err == nil && ch >= 1 && ch <= 32 && !seen[ch] {
				seen[ch] = true
				result = append(result, ch)
			}
		}
	}

	if len(result) == 0 {
		all := make([]int, 32)
		for i := 0; i < 32; i++ {
			all[i] = i + 1
		}
		return all
	}

	sort.Ints(result)
	return result
}

// parseOutputsList interpreta strings como "MASTER,AUX1,AUX2" ou "AUX1-AUX4" e retorna lista normalizada.
func parseOutputsList(raw string) []string {
	raw = strings.TrimSpace(strings.ToUpper(raw))
	if raw == "" || raw == "*" || raw == "ALL" || raw == "TODAS" || raw == "TODOS" {
		return append([]string(nil), allStandardOutputs...)
	}

	seen := make(map[string]bool)
	var result []string

	// Aceita separadores ';', ',', ':', espaços ou tabulações
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ';' || r == ',' || r == ':' || r == ' ' || r == '\t'
	})
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == "STEREO" || p == "MAIN" {
			p = "MASTER"
		}
		if strings.HasPrefix(p, "AUX") && strings.Contains(p, "-") {
			rangeParts := strings.Split(p, "-")
			if len(rangeParts) == 2 {
				sStr := strings.TrimPrefix(strings.TrimSpace(rangeParts[0]), "AUX")
				eStr := strings.TrimPrefix(strings.TrimSpace(rangeParts[1]), "AUX")
				start, err1 := strconv.Atoi(sStr)
				end, err2 := strconv.Atoi(eStr)
				if err1 == nil && err2 == nil {
					if start > end {
						start, end = end, start
					}
					for a := start; a <= end; a++ {
						if a >= 1 && a <= 8 {
							outName := fmt.Sprintf("AUX%d", a)
							if !seen[outName] {
								seen[outName] = true
								result = append(result, outName)
							}
						}
					}
				}
			}
			continue
		}

		for _, valid := range allStandardOutputs {
			if p == valid && !seen[p] {
				seen[p] = true
				result = append(result, p)
				break
			}
		}
	}

	if len(result) == 0 {
		return append([]string(nil), allStandardOutputs...)
	}

	return result
}

func (u User) GetAllowedChannelsList() []int {
	if !strings.EqualFold(u.Role, "BASIC") {
		all := make([]int, 32)
		for i := 0; i < 32; i++ {
			all[i] = i + 1
		}
		return all
	}
	return parseChannelsList(u.AllowedChannels)
}

func (u User) GetAllowedOutputsList() []string {
	if !strings.EqualFold(u.Role, "BASIC") {
		return append([]string(nil), allStandardOutputs...)
	}
	return parseOutputsList(u.AllowedOutputs)
}

func (u User) IsChannelAllowed(ch1Based int) bool {
	if !strings.EqualFold(u.Role, "BASIC") {
		return true
	}
	allowed := u.GetAllowedChannelsList()
	for _, ch := range allowed {
		if ch == ch1Based {
			return true
		}
	}
	return false
}

func (u User) IsOutputAllowed(outName string) bool {
	if !strings.EqualFold(u.Role, "BASIC") {
		return true
	}
	outUpper := strings.ToUpper(strings.TrimSpace(outName))
	if outUpper == "STEREO" || outUpper == "MAIN" {
		outUpper = "MASTER"
	}
	allowed := u.GetAllowedOutputsList()
	for _, out := range allowed {
		if out == outUpper {
			return true
		}
	}
	return false
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
		defaultContent := "nome,usuario,senha,papel,mix,canais,saidas,trocar_senha\nAdministrador,admin,admin,ADMIN,,,,false\n"
		if err := os.WriteFile(filePath, []byte(defaultContent), 0644); err != nil {
			log.Printf("Aviso: Falha ao inicializar %s: %v", filePath, err)
		} else {
			log.Printf("Arquivo '%s' criado com sucesso com usuário padrão (admin / admin / papel: ADMIN)", filePath)
		}
	}
}

// saveUsers persiste a lista de usuários no arquivo CSV de forma atômica e atualiza sessões ativas.
func saveUsers(users []User) error {
	filePath := getUsersCSVFile()
	usersLock.Lock()
	defer usersLock.Unlock()

	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.Write([]string{"nome", "usuario", "senha", "papel", "mix", "canais", "saidas", "trocar_senha"}); err != nil {
		return err
	}

	for _, u := range users {
		record := []string{
			u.Name,
			u.Username,
			u.Password,
			u.Role,
			u.MixID,
			u.AllowedChannels,
			u.AllowedOutputs,
			strconv.FormatBool(u.MustChangePassword),
		}
		if err := writer.Write(record); err != nil {
			return err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}

	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, buf.Bytes(), 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, filePath); err != nil {
		return err
	}

	// Atualiza as sessões ativas correspondentes em memória
	sessionsLock.Lock()
	for _, s := range sessions {
		for _, u := range users {
			if strings.EqualFold(s.User.Username, u.Username) {
				s.User.Name = u.Name
				s.User.Role = u.Role
				s.User.MixID = u.MixID
				s.User.AllowedChannels = u.AllowedChannels
				s.User.AllowedOutputs = u.AllowedOutputs
				s.User.MustChangePassword = u.MustChangePassword
				break
			}
		}
	}
	saveSessionsLocked()
	sessionsLock.Unlock()

	return nil
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
		role := strings.ToUpper(strings.TrimSpace(record[3]))
		mixID := ""
		if len(record) >= 5 {
			mixID = strings.TrimSpace(record[4])
		}
		allowedChannels := ""
		if len(record) >= 6 {
			allowedChannels = strings.TrimSpace(record[5])
		}
		allowedOutputs := ""
		mustChangePassword := false
		if len(record) == 7 {
			allowedOutputs = strings.TrimSpace(record[6])
		} else if len(record) == 8 {
			allowedOutputs = strings.TrimSpace(record[6])
			mustChangePassword = parseBoolFlag(record[7])
		} else if len(record) > 8 {
			lastCol := strings.ToLower(strings.TrimSpace(record[len(record)-1]))
			endIdx := len(record)
			if lastCol == "true" || lastCol == "false" || lastCol == "1" || lastCol == "0" || lastCol == "sim" || lastCol == "nao" || lastCol == "não" {
				mustChangePassword = parseBoolFlag(lastCol)
				endIdx = len(record) - 1
			}
			var outParts []string
			for _, p := range record[6:endIdx] {
				if trimmed := strings.TrimSpace(p); trimmed != "" {
					outParts = append(outParts, trimmed)
				}
			}
			allowedOutputs = strings.Join(outParts, ";")
		}

		// Normaliza delimitadores internos para ';' no carregamento
		allowedChannels = strings.ReplaceAll(allowedChannels, ",", ";")
		allowedOutputs = strings.ReplaceAll(allowedOutputs, ",", ";")

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

		if role == "PERFORMER" && mixID == "" {
			mixID = "1"
		}

		users = append(users, User{
			Name:               name,
			Username:           username,
			Password:           password,
			Role:               role,
			MixID:              mixID,
			AllowedChannels:    allowedChannels,
			AllowedOutputs:     allowedOutputs,
			MustChangePassword: mustChangePassword,
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

func getPostLoginRedirectTarget(r *http.Request, user User) string {
	if user.MustChangePassword {
		return "/change-password"
	}
	if strings.EqualFold(user.Role, "PERFORMER") {
		mixID := user.MixID
		if mixID == "" {
			mixID = "1"
		}
		return "/mix?id=" + mixID
	}
	if isMobileUserAgent(r) {
		return "/m"
	}
	return "/"
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if sess := getSessionFromRequest(r); sess != nil {
			http.Redirect(w, r, getPostLoginRedirectTarget(r, sess.User), http.StatusSeeOther)
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

		log.Printf("Usuário autenticado com sucesso: %s (%s, Papel: %s, TrocaSenha: %v)", user.Name, user.Username, user.Role, user.MustChangePassword)

		target := getPostLoginRedirectTarget(r, *user)

		if isJSON {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success":              true,
				"redirect":             target,
				"must_change_password": user.MustChangePassword,
				"user": map[string]interface{}{
					"name":                 user.Name,
					"username":             user.Username,
					"role":                 user.Role,
					"mix_id":               user.MixID,
					"must_change_password": user.MustChangePassword,
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
		"authenticated":        true,
		"name":                 sess.User.Name,
		"username":             sess.User.Username,
		"role":                 strings.ToUpper(sess.User.Role),
		"mix_id":               sess.User.MixID,
		"allowed_channels":     sess.User.GetAllowedChannelsList(),
		"allowed_outputs":      sess.User.GetAllowedOutputsList(),
		"must_change_password": sess.User.MustChangePassword,
	})
}

func handleChangePasswordPage(w http.ResponseWriter, r *http.Request) {
	sess := getSessionFromRequest(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.ServeFile(w, r, "change-password.html")
}

func handleAPIChangePassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Método não permitido"})
		return
	}

	sess := getSessionFromRequest(r)
	if sess == nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Requisição JSON inválida"})
		return
	}

	if req.CurrentPassword == "" || req.NewPassword == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Informe a senha atual e a nova senha."})
		return
	}

	if len(req.NewPassword) < 4 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "A nova senha deve ter pelo menos 4 caracteres."})
		return
	}

	if req.ConfirmPassword != "" && req.NewPassword != req.ConfirmPassword {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "A confirmação da nova senha não confere."})
		return
	}

	users, err := loadUsers()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	idx := -1
	for i := range users {
		if strings.EqualFold(users[i].Username, sess.User.Username) {
			idx = i
			break
		}
	}
	if idx == -1 {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Usuário não encontrado."})
		return
	}

	if users[idx].Password != req.CurrentPassword {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Senha atual incorreta."})
		return
	}

	if users[idx].MustChangePassword && req.NewPassword == req.CurrentPassword {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "No primeiro login, a nova senha deve ser diferente da senha inicial."})
		return
	}

	users[idx].Password = req.NewPassword
	users[idx].MustChangePassword = false

	if err := saveUsers(users); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	updatedUser := users[idx]
	target := getPostLoginRedirectTarget(r, updatedUser)
	log.Printf("[AUTH] Usuário '%s' alterou sua própria senha com sucesso.", updatedUser.Username)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"message":  "Senha alterada com sucesso!",
		"redirect": target,
	})
}

// handleAPIUsers gerencia a listagem e atualização de permissões de canais/saídas de usuários (exclusivo ADMIN)
func handleAPIUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	sess := requireAdminRole(w, r)
	if sess == nil {
		return
	}

	switch r.Method {
	case http.MethodGet:
		users, err := loadUsers()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		type SafeUserResp struct {
			Name               string   `json:"name"`
			Username           string   `json:"username"`
			Role               string   `json:"role"`
			MixID              string   `json:"mix_id,omitempty"`
			AllowedChannels    string   `json:"allowed_channels,omitempty"`
			AllowedOutputs     string   `json:"allowed_outputs,omitempty"`
			ChannelsList       []int    `json:"channels_list"`
			OutputsList        []string `json:"outputs_list"`
			MustChangePassword bool     `json:"must_change_password"`
		}
		var list []SafeUserResp
		for _, u := range users {
			list = append(list, SafeUserResp{
				Name:               u.Name,
				Username:           u.Username,
				Role:               u.Role,
				MixID:              u.MixID,
				AllowedChannels:    u.AllowedChannels,
				AllowedOutputs:     u.AllowedOutputs,
				ChannelsList:       u.GetAllowedChannelsList(),
				OutputsList:        u.GetAllowedOutputsList(),
				MustChangePassword: u.MustChangePassword,
			})
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"users":   list,
		})

	case http.MethodPost:
		var req struct {
			Username        string `json:"username"`
			AllowedChannels string `json:"allowed_channels"`
			AllowedOutputs  string `json:"allowed_outputs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Requisição JSON inválida"})
			return
		}

		users, err := loadUsers()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		found := false
		for i := range users {
			if strings.EqualFold(users[i].Username, req.Username) {
				cleanCh := strings.ReplaceAll(strings.TrimSpace(req.AllowedChannels), ",", ";")
				cleanOut := strings.ReplaceAll(strings.TrimSpace(req.AllowedOutputs), ",", ";")
				users[i].AllowedChannels = cleanCh
				users[i].AllowedOutputs = cleanOut
				found = true
				break
			}
		}

		if !found {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Usuário não encontrado"})
			return
		}

		if err := saveUsers(users); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		log.Printf("[ADMIN] Limites de canais/saídas do usuário '%s' atualizados por '%s'", req.Username, sess.User.Username)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Limites do usuário '%s' salvos com sucesso!", req.Username),
		})

	case http.MethodPut:
		var req struct {
			Name               string `json:"name"`
			Username           string `json:"username"`
			Password           string `json:"password"`
			Role               string `json:"role"`
			MixID              string `json:"mix_id"`
			MustChangePassword *bool  `json:"must_change_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Requisição JSON inválida"})
			return
		}

		name := strings.TrimSpace(req.Name)
		username := strings.TrimSpace(req.Username)
		password := req.Password
		role := strings.ToUpper(strings.TrimSpace(req.Role))
		mixID := strings.TrimSpace(req.MixID)

		if name == "" || username == "" || password == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Nome, usuário e senha são obrigatórios."})
			return
		}
		if strings.ContainsAny(username, " \t\r\n,;") {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "O login do usuário não pode conter espaços, vírgulas ou ponto-e-vírgula."})
			return
		}
		if len(password) < 4 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "A senha deve ter pelo menos 4 caracteres."})
			return
		}
		if role != "ADMIN" && role != "OPERATOR" && role != "BASIC" && role != "PERFORMER" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Papel inválido. Use ADMIN, OPERATOR, BASIC ou PERFORMER."})
			return
		}
		if role == "PERFORMER" && mixID == "" {
			mixID = "1"
		}

		users, err := loadUsers()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		for _, u := range users {
			if strings.EqualFold(u.Username, username) {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": fmt.Sprintf("O usuário '%s' já existe.", username)})
				return
			}
		}

		mustChange := true
		if req.MustChangePassword != nil {
			mustChange = *req.MustChangePassword
		}

		newUser := User{
			Name:               name,
			Username:           username,
			Password:           password,
			Role:               role,
			MixID:              mixID,
			MustChangePassword: mustChange,
		}
		users = append(users, newUser)

		if err := saveUsers(users); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		log.Printf("[ADMIN] Novo usuário '%s' (Papel: %s, TrocaSenha: %v) cadastrado por '%s'", newUser.Username, newUser.Role, newUser.MustChangePassword, sess.User.Username)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Usuário '%s' cadastrado com sucesso!", newUser.Username),
			"user": map[string]interface{}{
				"name":                 newUser.Name,
				"username":             newUser.Username,
				"role":                 newUser.Role,
				"mix_id":               newUser.MixID,
				"must_change_password": newUser.MustChangePassword,
			},
		})

	case http.MethodPatch:
		var req struct {
			Username           string `json:"username"`
			Name               string `json:"name"`
			Password           string `json:"password"`
			Role               string `json:"role"`
			MixID              string `json:"mix_id"`
			MustChangePassword *bool  `json:"must_change_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Requisição JSON inválida"})
			return
		}

		username := strings.TrimSpace(req.Username)
		if username == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Informe o usuário a atualizar."})
			return
		}

		users, err := loadUsers()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		idx := -1
		for i := range users {
			if strings.EqualFold(users[i].Username, username) {
				idx = i
				break
			}
		}
		if idx == -1 {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Usuário não encontrado."})
			return
		}

		if name := strings.TrimSpace(req.Name); name != "" {
			users[idx].Name = name
		}
		if role := strings.ToUpper(strings.TrimSpace(req.Role)); role != "" {
			if role != "ADMIN" && role != "OPERATOR" && role != "BASIC" && role != "PERFORMER" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Papel inválido. Use ADMIN, OPERATOR, BASIC ou PERFORMER."})
				return
			}
			users[idx].Role = role
		}
		if users[idx].Role == "PERFORMER" {
			if mixID := strings.TrimSpace(req.MixID); mixID != "" {
				users[idx].MixID = mixID
			} else if users[idx].MixID == "" {
				users[idx].MixID = "1"
			}
		} else if req.MixID != "" {
			users[idx].MixID = strings.TrimSpace(req.MixID)
		}
		if req.Password != "" {
			if len(req.Password) < 4 {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "A senha deve ter pelo menos 4 caracteres."})
				return
			}
			users[idx].Password = req.Password
			if req.MustChangePassword == nil {
				users[idx].MustChangePassword = true
			}
		}
		if req.MustChangePassword != nil {
			users[idx].MustChangePassword = *req.MustChangePassword
		}

		if err := saveUsers(users); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		log.Printf("[ADMIN] Usuário '%s' atualizado por '%s'", users[idx].Username, sess.User.Username)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Usuário '%s' atualizado com sucesso!", users[idx].Username),
		})

	case http.MethodDelete:
		username := strings.TrimSpace(r.URL.Query().Get("username"))
		if username == "" {
			var req struct {
				Username string `json:"username"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
				username = strings.TrimSpace(req.Username)
			}
		}
		if username == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Informe o usuário a excluir."})
			return
		}
		if strings.EqualFold(username, sess.User.Username) {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Você não pode excluir seu próprio usuário."})
			return
		}

		users, err := loadUsers()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		var filtered []User
		found := false
		for _, u := range users {
			if strings.EqualFold(u.Username, username) {
				found = true
				continue
			}
			filtered = append(filtered, u)
		}
		if !found {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Usuário não encontrado."})
			return
		}

		if err := saveUsers(filtered); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		log.Printf("[ADMIN] Usuário '%s' excluído por '%s'", username, sess.User.Username)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Usuário '%s' excluído com sucesso!", username),
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Método não permitido"})
	}
}

func handleMobile(w http.ResponseWriter, r *http.Request) {
	sess := getSessionFromRequest(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if sess.User.MustChangePassword {
		http.Redirect(w, r, "/change-password", http.StatusSeeOther)
		return
	}
	if strings.EqualFold(sess.User.Role, "PERFORMER") {
		mixID := sess.User.MixID
		if mixID == "" {
			mixID = "1"
		}
		http.Redirect(w, r, "/mix?id="+mixID, http.StatusSeeOther)
		return
	}
	http.ServeFile(w, r, "mobile.html")
}

func handleMix(w http.ResponseWriter, r *http.Request) {
	sess := getSessionFromRequest(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if sess.User.MustChangePassword {
		http.Redirect(w, r, "/change-password", http.StatusSeeOther)
		return
	}
	http.ServeFile(w, r, "mix.html")
}

func executeSystemShutdown() {
	time.Sleep(400 * time.Millisecond)
	log.Println("[!] Iniciando encerramento gracioso do serviço antes do desligamento da máquina (poweroff)...")
	stopSimulation()
	StopAutoSave()

	clientsLock.Lock()
	for client := range clients {
		client.Close()
		delete(clients, client)
	}
	clientsLock.Unlock()

	midiLock.Lock()
	closeMIDIBridgesLocked()
	if midiOut != nil {
		midiOut.Close()
		midiOut = nil
	}
	midiLock.Unlock()

	if httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}

	log.Println("[!] Serviço encerrado com sucesso. Executando poweroff da máquina...")
	poweroffCmds := [][]string{
		{"sudo", "-n", "systemctl", "poweroff", "-i"},
		{"sudo", "-n", "systemctl", "poweroff"},
		{"sudo", "-n", "/bin/systemctl", "poweroff"},
		{"sudo", "-n", "/usr/bin/systemctl", "poweroff"},
		{"sudo", "-n", "/sbin/poweroff"},
		{"sudo", "-n", "/usr/sbin/poweroff"},
		{"sudo", "-n", "/sbin/shutdown", "-h", "now"},
		{"sudo", "-n", "/usr/sbin/shutdown", "-h", "now"},
		{"systemctl", "poweroff", "-i"},
		{"systemctl", "poweroff"},
		{"poweroff"},
		{"shutdown", "-h", "now"},
	}
	shutdownSuccess := false
	for _, cmdArgs := range poweroffCmds {
		cmdStr := strings.Join(cmdArgs, " ")
		out, err := exec.Command(cmdArgs[0], cmdArgs[1:]...).CombinedOutput()
		if err == nil {
			log.Printf("[!] Comando de desligamento executado com sucesso: '%s'", cmdStr)
			shutdownSuccess = true
			break
		}
		log.Printf("[!] Tentativa '%s' falhou: %v (%s)", cmdStr, err, strings.TrimSpace(string(out)))
	}
	if !shutdownSuccess {
		log.Println("[ERRO] Nenhum comando de poweroff obteve sucesso. Verifique as permissões no /etc/sudoers.d/ ou Polkit.")
	}
	os.Exit(0)
}

func handleAPISystemShutdown(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Método não permitido. Use POST.",
		})
		return
	}

	sess := requireAdminRole(w, r)
	if sess == nil {
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Requisição JSON inválida.",
		})
		return
	}

	if strings.TrimSpace(req.Password) == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Digite sua senha para confirmar o desligamento.",
		})
		return
	}

	// Revalida a senha atual do administrador logado contra o arquivo de usuários
	if _, err := authenticateUser(sess.User.Username, req.Password); err != nil {
		// Verifica se o usuário da sessão ainda existe no CSV (ex: CSV foi editado com a sessão aberta)
		users, _ := loadUsers()
		userExists := false
		for _, u := range users {
			if strings.EqualFold(u.Username, sess.User.Username) {
				userExists = true
				break
			}
		}
		if !userExists {
			log.Printf("[ADMIN] Tentativa de desligamento negada: usuário da sessão '%s' não existe mais em '%s'", sess.User.Username, getUsersCSVFile())
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("O usuário '%s' da sua sessão atual não existe mais em usuarios.csv. Saia da sessão e faça login novamente.", sess.User.Username),
			})
			return
		}

		log.Printf("[ADMIN] Tentativa de desligamento negada para '%s': senha incorreta", sess.User.Username)
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Senha incorreta. O comando de desligamento não foi autorizado.",
		})
		return
	}

	if !canShutdownServer {
		log.Printf("[ADMIN] Comando de desligamento recebido de '%s' (senha validada), mas não executado porque o parâmetro '-podeDesligarServidor' não foi passado na inicialização.", sess.User.Username)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":           true,
			"command_received":  true,
			"shutdown_executed": false,
			"message":           "Comando recebido com sucesso! Porém, o servidor não será desligado porque o parâmetro '-podeDesligarServidor' não foi passado na inicialização.",
		})
		return
	}

	log.Printf("[ADMIN] Comando de desligamento autorizado por '%s'. Encerrando serviço graciosamente e desligando a máquina (poweroff)...", sess.User.Username)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":           true,
		"command_received":  true,
		"shutdown_executed": true,
		"message":           "Comando recebido com sucesso! Encerrando o serviço graciosamente e desligando o servidor (poweroff)...",
	})

	go shutdownExecFunc()
}

// isVolumeOrMuteParam determina se um comando SysEx (elemento/parâmetro) refere-se estritamente
// a volume (fader) ou chave ON/MUTE, permitido para usuários com perfil BASIC.
func isVolumeOrMuteParam(element, param byte) bool {
	switch element {
	case 0x1C: // Input Fader (Canal 1 a 32)
		return true
	case 0x1A: // Input Channel ON / MUTE (Canal 1 a 32)
		return true
	case 0x4F, 0x4D: // Stereo Master Out Fader (0x4F) / Stereo Master ON (0x4D)
		return true
	case 0x4B: // Stereo Master ON (0x4B)
		return true
	case 0x39, 0x31: // Aux Master Fader (Aux 1 a 8)
		return true
	case 0x36, 0x2F: // Aux Master ON (Aux 1 a 8)
		return true
	case 0x23: // Aux Sends por canal
		// pp % 3 == 0: Chave ON/MUTE do envio
		// pp % 3 == 2: Nível (Fader) do envio
		// pp % 3 == 1: Pre/Post (não é volume nem mute)
		return (param%3 == 0) || (param%3 == 2)
	default:
		return false
	}
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	sess := getSessionFromRequest(r)
	if sess == nil {
		log.Printf("Tentativa de conexão WebSocket não autorizada de %s rejeitada", r.RemoteAddr)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if sess.User.MustChangePassword {
		log.Printf("Tentativa de conexão WebSocket bloqueada para '%s': troca de senha obrigatória pendente", sess.User.Username)
		http.Error(w, "Password change required", http.StatusForbidden)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Erro no upgrade WebSocket: %v", err)
		return
	}
	defer func() {
		setGRSubscription(conn, "")
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

	// Envia informação inicial (porta, status da simulação e mute groups)
	conn.WriteJSON(WSMessage{
		Type:       "init",
		Port:       pName,
		Running:    &runningCopy,
		Hex:        "Conectado ao console Yamaha 01V96V2",
		MuteGroups: ListMuteGroups(),
	})

	for {
		var msg WSMessage
		if err := conn.ReadJSON(&msg); err != nil {
			log.Println("Cliente WebSocket desconectado:", err)
			break
		}

		userRole := strings.ToUpper(sess.User.Role)

		// Movimento do Joystick 3 Eixos da Mix Pessoal ("Eu", "Vozes", "Outros")
		if msg.Action == "personal_mix_joystick" {
			targetMixID := sanitizeSlug(msg.MixID)
			if userRole == "PERFORMER" {
				myMix := GetPersonalMixForPerformer(sess.User.Username)
				if myMix == nil && sess.User.MixID != "" {
					myMix, _ = GetPersonalMix(sess.User.MixID)
				}
				if myMix == nil || (targetMixID != "" && myMix.ID != targetMixID) {
					log.Printf("WebSocket: acesso negado à mix '%s' para PERFORMER '%s'", targetMixID, sess.User.Username)
					continue
				}
				targetMixID = myMix.ID
			}
			if targetMixID != "" {
				if _, _, _, err := ApplyPersonalMixJoystick(targetMixID, msg.JoyX, msg.JoyY, msg.Persist); err != nil {
					log.Printf("WebSocket: erro ao aplicar joystick na mix '%s': %v", targetMixID, err)
				}
			}
			continue
		}

		if msg.Action == "personal_mix_master" {
			targetMixID := sanitizeSlug(msg.MixID)
			if userRole == "PERFORMER" {
				myMix := GetPersonalMixForPerformer(sess.User.Username)
				if myMix == nil && sess.User.MixID != "" {
					myMix, _ = GetPersonalMix(sess.User.MixID)
				}
				if myMix == nil || (targetMixID != "" && myMix.ID != targetMixID) {
					log.Printf("WebSocket: acesso negado ao master da mix '%s' para PERFORMER '%s'", targetMixID, sess.User.Username)
					continue
				}
				targetMixID = myMix.ID
			}
			if targetMixID != "" {
				if _, _, err := ApplyPersonalMixMaster(targetMixID, msg.MasterLevel, msg.Persist); err != nil {
					log.Printf("WebSocket: erro ao aplicar master na mix '%s': %v", targetMixID, err)
				}
			}
			continue
		}

		// 1. Usuário PERFORMER não possui permissão para emitir comandos no console principal
		if userRole == "PERFORMER" {
			log.Printf("WebSocket: comando ignorado para usuário PERFORMER '%s'", sess.User.Username)
			continue
		}

		// Acionamento de Mute Group da aplicação (liberado para ADMIN, OPERATOR e BASIC)
		if msg.Action == "toggle_mute_group" {
			if _, _, err := ToggleMuteGroup(msg.MuteGroupID); err != nil {
				log.Printf("WebSocket: erro ao alternar Mute Group %d: %v", msg.MuteGroupID, err)
			}
			continue
		}
		if msg.Action == "set_mute_group" {
			if _, _, err := SetMuteGroupState(msg.MuteGroupID, msg.Value != 0); err != nil {
				log.Printf("WebSocket: erro ao definir Mute Group %d: %v", msg.MuteGroupID, err)
			}
			continue
		}

		// Assinatura sob demanda do medidor de Gain Reduction (apenas ADMIN/OPERATOR com tela do compressor aberta)
		if msg.Action == "subscribe_gr" {
			if userRole == "ADMIN" || userRole == "OPERATOR" {
				setGRSubscription(conn, msg.Target)
			}
			continue
		}
		if msg.Action == "unsubscribe_gr" {
			setGRSubscription(conn, "")
			continue
		}

		if msg.Action != "" && msg.Element == 0 {
			log.Printf("WebSocket: ação '%s' desconhecida/ignorada", msg.Action)
			continue
		}

		// 2. Ações de simulação ou sincronização geral são exclusivas de administradores (ADMIN)
		if msg.Action == "toggle_sim" || msg.Action == "sync_mixer" || msg.Action == "sync_main" {
			if userRole != "ADMIN" {
				log.Printf("WebSocket: ação administrativa '%s' negada para usuário '%s' (Papel: %s)", msg.Action, sess.User.Username, userRole)
				continue
			}
		}

		// 3. Usuário BASIC pode alterar apenas volumes e mute (sem ações de sync/simulação, sem EQ/Comp/Gate/Pan)
		// e apenas nas entradas e saídas delimitadas pelo administrador para ele
		if userRole == "BASIC" {
			if msg.Action != "" {
				log.Printf("WebSocket: ação '%s' negada para usuário BASIC '%s'", msg.Action, sess.User.Username)
				continue
			}
			if !isVolumeOrMuteParam(msg.Element, msg.Param) {
				log.Printf("WebSocket: alteração de parâmetro [elem: 0x%02X, param: 0x%02X] negada para usuário BASIC '%s'", msg.Element, msg.Param, sess.User.Username)
				continue
			}

			// Validação de limites de Canais de Entrada e Saídas:
			// A) Fader ou Mute de canal de entrada (0x1C ou 0x1A)
			if msg.Element == 0x1C || msg.Element == 0x1A {
				ch1Based := int(msg.Channel) + 1
				if !sess.User.IsChannelAllowed(ch1Based) {
					log.Printf("WebSocket: acesso ao canal CH %02d negado para usuário BASIC '%s' (fora do limite configurado)", ch1Based, sess.User.Username)
					continue
				}
			}

			// B) Fader ou Mute de envio para Auxiliar (0x23)
			if msg.Element == 0x23 {
				ch1Based := int(msg.Channel) + 1
				auxNum := int(msg.Param/3) + 1
				auxName := fmt.Sprintf("AUX%d", auxNum)
				if !sess.User.IsChannelAllowed(ch1Based) {
					log.Printf("WebSocket: envio do canal CH %02d negado para usuário BASIC '%s' (fora do limite)", ch1Based, sess.User.Username)
					continue
				}
				if !sess.User.IsOutputAllowed(auxName) {
					log.Printf("WebSocket: envio para saída '%s' negado para usuário BASIC '%s' (fora do limite)", auxName, sess.User.Username)
					continue
				}
			}

			// C) Stereo Master Fader ou ON (0x4F, 0x4D, 0x4B)
			if msg.Element == 0x4F || msg.Element == 0x4D || msg.Element == 0x4B {
				if !sess.User.IsOutputAllowed("MASTER") {
					log.Printf("WebSocket: controle da saída MASTER negado para usuário BASIC '%s' (fora do limite)", sess.User.Username)
					continue
				}
			}

			// D) Aux Master Fader ou ON (0x39, 0x31, 0x36, 0x2F)
			if msg.Element == 0x39 || msg.Element == 0x31 || msg.Element == 0x36 || msg.Element == 0x2F {
				auxNum := int(msg.Channel) + 1
				auxName := fmt.Sprintf("AUX%d", auxNum)
				if !sess.User.IsOutputAllowed(auxName) {
					log.Printf("WebSocket: controle da saída '%s' negado para usuário BASIC '%s' (fora do limite)", auxName, sess.User.Username)
					continue
				}
			}
		}

		// Ação de alternar simulação solicitada pela tela
		if msg.Action == "toggle_sim" {
			toggleSimulation()
			continue
		}

		// Ação de solicitar sincronização da tela principal (Canais e Masters)
		if msg.Action == "sync_mixer" || msg.Action == "sync_main" {
			go func() {
				_ = RequestMainViewSync(15 * time.Millisecond)
			}()
			continue
		}

		// Ação de solicitar sincronização sob demanda dos envios de um Auxiliar (Sends on Fader)
		if msg.Action == "sync_aux" {
			auxNum := msg.Aux
			if auxNum < 1 || auxNum > 8 {
				auxNum = int(msg.Channel) + 1
			}
			if auxNum >= 1 && auxNum <= 8 {
				go func(a int) {
					_ = RequestAuxSendsSync(a-1, 15*time.Millisecond)
				}(auxNum)
			}
			continue
		}

		// Ação de solicitar sincronização sob demanda do Rack de Efeitos de um Canal (EQ, Comp, Gate)
		if msg.Action == "sync_fx" {
			target := msg.Target
			if target == "" {
				target = fmt.Sprintf("%d", msg.Channel)
			}
			go func(t string) {
				_ = RequestChannelFXSync(t, 15*time.Millisecond)
			}(target)
			continue
		}

		// Comando de alteração de fader ou mute vindo da tela
		ApplyParamToState(msg.Element, msg.Param, msg.Channel, msg.Value)
		sysex := Build01V96SysEx(msg.Element, msg.Param, msg.Channel, msg.Value)
		hexStr := fmt.Sprintf("% X", sysex)

		targetDesc := describe01V96Param(msg.Element, msg.Param, msg.Channel, msg.Value)

		fmt.Printf("WS -> SysEx: %s | %s\n", hexStr, targetDesc)

		// Aplica throttle antes de enviar ao barramento MIDI e ao WebSocket.
		// Eventos ON/MUTE/Bypass discretos, seletores de filtro/Q ou limites de fader/freq passam direto sem throttling.
		// Para movimentos contínuos dentro da janela de 20ms, allowOrSchedule agenda o envio do valor final (trailing edge)
		// para garantir que a última posição do fader/knob nunca se perca.
		isFaderElem := msg.Element == 0x1C || msg.Element == 0x4F || msg.Element == 0x39 || msg.Element == 0x1D || (msg.Element == 0x23 && msg.Param%3 == 2)
		isEqElem := msg.Element == 0x20 || msg.Element == 0x35 || msg.Element == 0x50
		isDiscrete := msg.Element == 0x1A ||
			msg.Element == 0x4D || msg.Element == 0x36 ||
			(msg.Element == 0x23 && (msg.Param%3 == 0 || msg.Param%3 == 1)) ||
			(isEqElem && (msg.Param == 0x0F || msg.Param == 0x00 || msg.Param == 0x01 || msg.Param == 0x0B || msg.Param == 0x04 || msg.Param == 0x0E)) ||
			(isEqElem && (msg.Param == 0x03 || msg.Param == 0x0D) && (msg.Value == 0 || msg.Value == 1)) ||
			(isEqElem && (msg.Param == 0x02 || msg.Param == 0x06 || msg.Param == 0x09 || msg.Param == 0x0C) && (msg.Value <= 0 || msg.Value >= 124)) ||
			((msg.Element == 0x1F || msg.Element == 0x36 || msg.Element == 0x51) && (msg.Param == 0x01 || msg.Param == 0x00)) ||
			(msg.Element == 0x21 && msg.Param == 0x00) ||
			(isFaderElem && (msg.Value <= 0 || msg.Value == 823 || msg.Value >= 1023))

		canMIDI, canWS := true, true
		if !isDiscrete {
			elemCopy, paramCopy, chCopy := msg.Element, msg.Param, msg.Channel
			canMIDI, canWS = throttler.allowOrSchedule(elemCopy, paramCopy, chCopy, msg.Value, func(finalVal int) {
				finalSysex := Build01V96SysEx(elemCopy, paramCopy, chCopy, finalVal)
				finalHex := fmt.Sprintf("% X", finalSysex)

				markSentEcho(elemCopy, paramCopy, chCopy, finalVal)

				midiLock.Lock()
				if err := sendMIDISysExLocked(finalSysex); err != nil {
					log.Printf("Erro ao enviar MIDI (flush): %v", err)
				}
				midiLock.Unlock()

				broadcastWS(WSMessage{
					Type:    "param",
					Element: elemCopy,
					Param:   paramCopy,
					Channel: chCopy,
					Value:   finalVal,
					Hex:     finalHex,
				})
			})
		}

		// Envia para a saída MIDI física / bridges ALSA
		if canMIDI {
			markSentEcho(msg.Element, msg.Param, msg.Channel, msg.Value)

			midiLock.Lock()
			if err := sendMIDISysExLocked(sysex); err != nil {
				log.Printf("Erro ao enviar MIDI: %v", err)
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

// findMatchingInPort busca a porta de entrada MIDI correspondente à porta de saída selecionada
func findMatchingInPort(ins []drivers.In, out drivers.Out) drivers.In {
	if out == nil || len(ins) == 0 {
		return nil
	}
	outStr := out.String()
	// 1. Coincidência exata do nome da porta
	for _, in := range ins {
		if in.String() == outStr {
			return in
		}
	}
	// 2. Coincidência pelo prefixo antes de ':' ou pelo nome do dispositivo/interface
	outParts := strings.Split(outStr, ":")
	if len(outParts) > 0 && len(strings.TrimSpace(outParts[0])) > 0 {
		prefix := strings.ToLower(strings.TrimSpace(outParts[0]))
		for _, in := range ins {
			if strings.Contains(strings.ToLower(in.String()), prefix) {
				return in
			}
		}
	}
	// 3. Fallback: se houver apenas uma entrada disponível no sistema
	if len(ins) == 1 {
		return ins[0]
	}
	return nil
}

// selectMIDIPorts gerencia a escolha da porta MIDI (via flag ou interativamente no terminal antes de iniciar o serviço)
// e emparelha automaticamente a saída e a entrada correspondentes para comunicação bidirecional.
func selectMIDIPorts(outs []drivers.Out, ins []drivers.In, flagPort string) (drivers.Out, drivers.In) {
	pairAndOpen := func(out drivers.Out) (drivers.Out, drivers.In) {
		if out == nil {
			return nil, nil
		}
		in := findMatchingInPort(ins, out)
		if in != nil {
			if err := in.Open(); err != nil {
				log.Printf("Aviso: não foi possível abrir a porta de entrada MIDI correspondente '%s': %v", in.String(), err)
				in = nil
			}
		}
		return out, in
	}

	// Se foi especificado por flag de linha de comando
	if flagPort != "" {
		trimmed := strings.TrimSpace(flagPort)
		if strings.EqualFold(trimmed, "s") || strings.EqualFold(trimmed, "sim") || strings.EqualFold(trimmed, "none") {
			log.Println("Modo de simulação selecionado via parâmetro de linha de comando.")
			return nil, nil
		}

		// Tenta interpretar como índice numérico
		if idx, err := strconv.Atoi(trimmed); err == nil {
			if idx >= 0 && idx < len(outs) {
				out := outs[idx]
				if err := out.Open(); err != nil {
					log.Printf("Erro ao abrir porta %d (%s): %v. Prosseguindo para seleção interativa.", idx, out.String(), err)
				} else {
					return pairAndOpen(out)
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
					return pairAndOpen(out)
				}
			}
			log.Printf("Nenhuma porta encontrada contendo o termo '%s'.", trimmed)
		}
		// Se -port foi informado explicitamente e não é simulação, não silenciar nem cair em simulação sem TTY
		if fi, err := os.Stdin.Stat(); err != nil || (fi.Mode()&os.ModeCharDevice) == 0 || len(outs) == 0 {
			log.Fatalf("Erro: porta MIDI '%s' não encontrada ou indisponível. Encerrando serviço (use -port sim ou -sim apenas se desejar modo simulação).", trimmed)
		}
	}

	// Caso não haja portas MIDI disponíveis no sistema
	if len(outs) == 0 {
		fmt.Println("\n⚠️  Nenhuma porta MIDI de saída foi encontrada no sistema.")
		fmt.Println("O serviço funcionará em modo de simulação (apenas gerando SysEx nos logs e na tela).")
		return nil, nil
	}

	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Println("\n==============================================================")
		fmt.Println("  Yamaha 01V96 Controller — Seleção de Porta MIDI de Saída/Entrada")
		fmt.Println("==============================================================")
		fmt.Println("Portas MIDI disponíveis:")
		for i, out := range outs {
			defaultMark := ""
			if i == 0 {
				defaultMark = " (Padrão)"
			}
			matchedInStr := ""
			if matchedIn := findMatchingInPort(ins, out); matchedIn != nil {
				matchedInStr = fmt.Sprintf(" [IN: %s]", matchedIn.String())
			}
			fmt.Printf("  [%d] %s%s%s\n", i, out.String(), matchedInStr, defaultMark)
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
				return nil, nil
			}
			return pairAndOpen(out)
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
			return nil, nil
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

		return pairAndOpen(out)
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
		{name: "AUX1", element: 0x23, param: 0x02},
		{name: "AUX2", element: 0x23, param: 0x05},
		{name: "AUX3", element: 0x23, param: 0x08},
		{name: "AUX4", element: 0x23, param: 0x0B},
		{name: "AUX5", element: 0x23, param: 0x0E},
		{name: "AUX6", element: 0x23, param: 0x11},
		{name: "AUX7", element: 0x23, param: 0x14},
		{name: "AUX8", element: 0x23, param: 0x17},
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
							if err := sendMIDISysExLocked(sysex); err != nil {
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
	flagPodeDesligar := flag.Bool("podeDesligarServidor", false, "Permite que o botão administrativo 'Desligar' encerre o serviço e desligue a máquina (poweroff)")
	flag.Parse()

	canShutdownServer = *flagPodeDesligar

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
	ins := midi.GetInPorts()
	var in drivers.In
	midiOut, in = selectMIDIPorts(outs, ins, *flagPort)

	if midiOut != nil {
		defer func() {
			midiLock.Lock()
			closeMIDIBridgesLocked()
			midiOut.Close()
			midiLock.Unlock()
		}()
		midiPortName = midiOut.String()
		log.Printf("Conectado na saída MIDI: %s", midiPortName)
		setupMIDIBridges(outs, midiOut)
	} else {
		midiPortName = "Modo Simulação (sem porta MIDI física)"
		log.Println("Rodando em modo simulação (nenhuma porta MIDI física conectada).")
	}

	if in != nil {
		log.Printf("Conectado na entrada MIDI: %s", in.String())
		stopFn, err := startMIDIInListener(in)
		if err != nil {
			log.Printf("Aviso: erro ao iniciar escuta MIDI IN: %v", err)
		} else {
			defer func() {
				if stopFn != nil {
					stopFn()
				}
				in.Close()
			}()
			log.Printf("Escuta MIDI IN ativada em '%s'. Alterações físicas na mesa serão refletidas no navegador.", in.String())

			// Solicita o estado inicial da mesa física em segundo plano
			go func() {
				time.Sleep(300 * time.Millisecond)
				if err := RequestMixerInitialState(15 * time.Millisecond); err != nil {
					log.Printf("Aviso na sincronização inicial com a mesa: %v", err)
				}
			}()
		}
	}

	// Se o modo simulação foi solicitado por linha de comando, inicia os faders
	if *flagSimulate {
		startSimulation(midiOut)
	}

	http.HandleFunc("/login", handleLogin)
	http.HandleFunc("/logout", handleLogout)
	http.HandleFunc("/change-password", handleChangePasswordPage)
	http.HandleFunc("/api/change-password", handleAPIChangePassword)
	http.HandleFunc("/api/me", handleMe)
	http.HandleFunc("/api/users", handleAPIUsers)
	http.HandleFunc("/api/scenes", handleAPIScenes)
	http.HandleFunc("/api/scenes/", handleAPISceneDetail)
	http.HandleFunc("/api/scenes/import", handleAPIImportScene)
	http.HandleFunc("/api/personal-mixes", handleAPIPersonalMixes)
	http.HandleFunc("/api/personal-mixes/", handleAPIPersonalMixDetail)
	http.HandleFunc("/api/mute-groups", handleAPIMuteGroups)
	http.HandleFunc("/api/mute-groups/", handleAPIMuteGroupDetail)
	http.HandleFunc("/api/state/status", handleAPIStateStatus)
	http.HandleFunc("/api/state/restore-last", handleAPIRestoreLastState)
	http.HandleFunc("/api/state/sync-mixer", handleAPISyncMixer)
	http.HandleFunc("/api/state", handleAPIGetCurrentState)
	http.HandleFunc("/api/system/shutdown", handleAPISystemShutdown)
	http.HandleFunc("/mix", handleMix)
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
		if sess.User.MustChangePassword {
			http.Redirect(w, r, "/change-password", http.StatusSeeOther)
			return
		}
		if strings.EqualFold(sess.User.Role, "PERFORMER") {
			mixID := sess.User.MixID
			if mixID == "" {
				mixID = "1"
			}
			http.Redirect(w, r, "/mix?id="+mixID, http.StatusSeeOther)
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
		if midiInStopFunc != nil {
			midiInStopFunc()
		}
		os.Exit(0)
	}()

	log.Printf("Servidor Console Web rodando em http://%s (podeDesligarServidor=%v)", *flagAddr, canShutdownServer)
	httpServer = &http.Server{Addr: *flagAddr, Handler: nil}
	if err := httpServer.ListenAndServe(); err != nil {
		if err == http.ErrServerClosed {
			// Aguarda a goroutine executeSystemShutdown concluir o poweroff e chamar os.Exit(0)
			select {}
		}
		log.Fatal(err)
	}
}
