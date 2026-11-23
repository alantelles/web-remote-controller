package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ==========================================
// MODELO DE DADOS: MUTE GROUPS DA APLICAÇÃO
// ==========================================

// NumMuteGroups define a quantidade fixa de Mute Groups gerenciados pela aplicação.
const NumMuteGroups = 4

// MuteGroup representa um grupo de mute definido e controlado pela aplicação.
// Regra fundamental: um canal (1 a 32) pode pertencer a vários Mute Groups simultaneamente,
// e não deve ser desmutado ao desativar um grupo caso pertença a qualquer outro grupo ainda ativo (mutado).
type MuteGroup struct {
	ID        int       `json:"id"`         // 1 a NumMuteGroups (1 a 4)
	Name      string    `json:"name"`       // Nome amigável (ex: "MUTE 1", "Banda", "Vozes")
	Channels  []int     `json:"channels"`   // Canais de entrada cadastrados no grupo (1 a 32)
	Muted     bool      `json:"muted"`      // true = Grupo Ativo (mutado), false = Grupo Inativo
	UpdatedAt time.Time `json:"updated_at"` // Última modificação ou acionamento
}

var (
	muteGroupsLock     sync.RWMutex
	muteGroups         [NumMuteGroups]*MuteGroup
	muteGroupsFilePath = filepath.Join("data", "mute_groups.json")
)

// newDefaultMuteGroups cria os 4 grupos de mute padrão limpos e desativados.
func newDefaultMuteGroups() [NumMuteGroups]*MuteGroup {
	var groups [NumMuteGroups]*MuteGroup
	now := time.Now()
	for i := 0; i < NumMuteGroups; i++ {
		groups[i] = &MuteGroup{
			ID:        i + 1,
			Name:      fmt.Sprintf("MUTE %d", i+1),
			Channels:  []int{},
			Muted:     false,
			UpdatedAt: now,
		}
	}
	return groups
}

// InitMuteGroupManager configura o arquivo de persistência e carrega os Mute Groups do disco.
func InitMuteGroupManager(baseDir string) {
	muteGroupsLock.Lock()
	if strings.TrimSpace(baseDir) != "" {
		muteGroupsFilePath = filepath.Join(baseDir, "mute_groups.json")
	} else {
		muteGroupsFilePath = filepath.Join("data", "mute_groups.json")
	}
	muteGroups = newDefaultMuteGroups()
	filePath := muteGroupsFilePath
	muteGroupsLock.Unlock()

	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		log.Printf("Aviso: Falha ao criar diretório para mute groups '%s': %v", filepath.Dir(filePath), err)
	}

	if err := loadMuteGroupsFromDisk(); err != nil {
		log.Printf("Aviso ao carregar mute groups de '%s': %v", filePath, err)
	}
}

func loadMuteGroupsFromDisk() error {
	muteGroupsLock.Lock()
	defer muteGroupsLock.Unlock()

	data, err := os.ReadFile(muteGroupsFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			muteGroups = newDefaultMuteGroups()
			return nil
		}
		return err
	}

	var list []*MuteGroup
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("formato inválido em %s: %w", muteGroupsFilePath, err)
	}

	muteGroups = newDefaultMuteGroups()
	for _, g := range list {
		if g == nil || g.ID < 1 || g.ID > NumMuteGroups {
			continue
		}
		cleanCh, err := normalizeMuteGroupChannels(g.Channels)
		if err != nil {
			cleanCh = []int{}
		}
		name := strings.TrimSpace(g.Name)
		if name == "" {
			name = fmt.Sprintf("MUTE %d", g.ID)
		}
		muteGroups[g.ID-1] = &MuteGroup{
			ID:        g.ID,
			Name:      name,
			Channels:  cleanCh,
			Muted:     g.Muted,
			UpdatedAt: g.UpdatedAt,
		}
	}

	log.Printf("Mute Groups: %d grupo(s) carregado(s) de '%s'", NumMuteGroups, muteGroupsFilePath)
	return nil
}

// saveMuteGroupsLocked persiste atomicamente os Mute Groups em disco.
// Deve ser chamado com muteGroupsLock adquirido.
func saveMuteGroupsLocked() error {
	list := make([]*MuteGroup, 0, NumMuteGroups)
	for i := 0; i < NumMuteGroups; i++ {
		if muteGroups[i] != nil {
			list = append(list, muteGroups[i])
		}
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("erro ao serializar mute groups: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(muteGroupsFilePath), 0755); err != nil {
		return fmt.Errorf("erro ao criar diretório de mute groups: %w", err)
	}

	tmpFile := muteGroupsFilePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("erro ao gravar arquivo temporário de mute groups: %w", err)
	}
	if err := os.Rename(tmpFile, muteGroupsFilePath); err != nil {
		return fmt.Errorf("erro ao atualizar arquivo final de mute groups: %w", err)
	}
	return nil
}

// normalizeMuteGroupChannels valida canais de 1 a 32, remove duplicatas dentro do mesmo grupo e ordena.
// Um mesmo canal PODE pertencer a múltiplos Mute Groups distintos.
func normalizeMuteGroupChannels(channels []int) ([]int, error) {
	seen := make(map[int]bool, len(channels))
	out := make([]int, 0, len(channels))
	for _, ch := range channels {
		if ch < 1 || ch > 32 {
			return nil, fmt.Errorf("canal %d inválido (permitido: 1 a 32)", ch)
		}
		if seen[ch] {
			continue
		}
		seen[ch] = true
		out = append(out, ch)
	}
	sort.Ints(out)
	return out, nil
}

func cloneMuteGroup(g *MuteGroup) *MuteGroup {
	if g == nil {
		return nil
	}
	cp := *g
	cp.Channels = make([]int, len(g.Channels))
	copy(cp.Channels, g.Channels)
	return &cp
}

// ListMuteGroups retorna uma cópia thread-safe de todos os Mute Groups (1 a 4).
func ListMuteGroups() []*MuteGroup {
	muteGroupsLock.RLock()
	defer muteGroupsLock.RUnlock()

	out := make([]*MuteGroup, 0, NumMuteGroups)
	for i := 0; i < NumMuteGroups; i++ {
		if muteGroups[i] == nil {
			out = append(out, &MuteGroup{
				ID:       i + 1,
				Name:     fmt.Sprintf("MUTE %d", i+1),
				Channels: []int{},
				Muted:    false,
			})
		} else {
			out = append(out, cloneMuteGroup(muteGroups[i]))
		}
	}
	return out
}

// GetMuteGroup retorna uma cópia thread-safe de um Mute Group específico pelo ID (1 a 4).
func GetMuteGroup(groupID int) (*MuteGroup, error) {
	if groupID < 1 || groupID > NumMuteGroups {
		return nil, fmt.Errorf("mute group %d inválido (disponíveis: 1 a %d)", groupID, NumMuteGroups)
	}
	muteGroupsLock.RLock()
	defer muteGroupsLock.RUnlock()

	g := muteGroups[groupID-1]
	if g == nil {
		return &MuteGroup{
			ID:       groupID,
			Name:     fmt.Sprintf("MUTE %d", groupID),
			Channels: []int{},
			Muted:    false,
		}, nil
	}
	return cloneMuteGroup(g), nil
}

// isChannelInAnyActiveGroupLocked verifica se o canal (1 a 32) faz parte de algum Mute Group
// atualmente ativo (Muted == true), opcionalmente ignorando excludeGroupID (0 para não ignorar nenhum).
// Deve ser chamado com muteGroupsLock adquirido.
func isChannelInAnyActiveGroupLocked(ch int, excludeGroupID int) bool {
	for i := 0; i < NumMuteGroups; i++ {
		g := muteGroups[i]
		if g == nil || !g.Muted || g.ID == excludeGroupID {
			continue
		}
		for _, memberCh := range g.Channels {
			if memberCh == ch {
				return true
			}
		}
	}
	return false
}

// IsChannelMutedByActiveMuteGroup informa se um canal (1 a 32) pertence a pelo menos um Mute Group ativo no momento.
func IsChannelMutedByActiveMuteGroup(ch int) bool {
	muteGroupsLock.RLock()
	defer muteGroupsLock.RUnlock()
	return isChannelInAnyActiveGroupLocked(ch, 0)
}

type channelMuteTransition struct {
	channel int // 1 a 32
	onValue int // 0 = MUTE, 1 = ON
}

// emitChannelMuteTransition atualiza o estado em memória, envia o SysEx 0x1A sem throttle ao hardware
// e faz broadcast via WebSocket para todos os clientes conectados.
func emitChannelMuteTransition(ch int, onValue int) {
	if ch < 1 || ch > 32 {
		return
	}
	if onValue != 0 {
		onValue = 1
	}
	chIdx := byte(ch - 1)
	elem := byte(0x1A)
	param := byte(0x00)

	ApplyParamToState(elem, param, chIdx, onValue)
	sysex := Build01V96SysEx(elem, param, chIdx, onValue)
	hexStr := fmt.Sprintf("% X", sysex)

	markSentEcho(elem, param, chIdx, onValue)

	midiLock.Lock()
	if err := sendMIDISysExLocked(sysex); err != nil {
		log.Printf("Erro ao enviar MIDI (Mute Group CH %02d): %v", ch, err)
	}
	midiLock.Unlock()

	broadcastWS(WSMessage{
		Type:    "param",
		Element: elem,
		Param:   param,
		Channel: chIdx,
		Value:   onValue,
		Hex:     hexStr,
	})
}

func broadcastMuteGroupsState() {
	broadcastWS(WSMessage{
		Type:       "mute_groups",
		MuteGroups: ListMuteGroups(),
	})
}

// SetMuteGroupState ativa (muted=true) ou desativa (muted=false) um Mute Group (1 a 4).
// - Ao ativar (muted=true): todos os canais do grupo são mutados (On = 0).
// - Ao desativar (muted=false): cada canal do grupo só é desmutado (On = 1) se NÃO pertencer
//   a nenhum outro Mute Group que continue ativo (mutado).
func SetMuteGroupState(groupID int, muted bool) (*MuteGroup, []channelMuteTransition, error) {
	if groupID < 1 || groupID > NumMuteGroups {
		return nil, nil, fmt.Errorf("mute group %d inválido (disponíveis: 1 a %d)", groupID, NumMuteGroups)
	}

	muteGroupsLock.Lock()
	if muteGroups[groupID-1] == nil {
		muteGroups[groupID-1] = &MuteGroup{
			ID:       groupID,
			Name:     fmt.Sprintf("MUTE %d", groupID),
			Channels: []int{},
		}
	}
	g := muteGroups[groupID-1]
	g.Muted = muted
	g.UpdatedAt = time.Now()

	transitions := make([]channelMuteTransition, 0, len(g.Channels))
	if muted {
		// Ativar Mute Group: muta todos os canais cadastrados no grupo
		for _, ch := range g.Channels {
			transitions = append(transitions, channelMuteTransition{
				channel: ch,
				onValue: 0,
			})
		}
	} else {
		// Desativar Mute Group: só desmuta os canais que NÃO pertencem a outro grupo ativo
		for _, ch := range g.Channels {
			if !isChannelInAnyActiveGroupLocked(ch, groupID) {
				transitions = append(transitions, channelMuteTransition{
					channel: ch,
					onValue: 1,
				})
			}
		}
	}

	if err := saveMuteGroupsLocked(); err != nil {
		log.Printf("Aviso: erro ao salvar estado dos mute groups em disco: %v", err)
	}
	resultCopy := cloneMuteGroup(g)
	muteGroupsLock.Unlock()

	for _, tr := range transitions {
		emitChannelMuteTransition(tr.channel, tr.onValue)
	}
	broadcastMuteGroupsState()

	return resultCopy, transitions, nil
}

// ToggleMuteGroup inverte o estado atual (ativo/inativo) do Mute Group indicado.
func ToggleMuteGroup(groupID int) (*MuteGroup, []channelMuteTransition, error) {
	if groupID < 1 || groupID > NumMuteGroups {
		return nil, nil, fmt.Errorf("mute group %d inválido (disponíveis: 1 a %d)", groupID, NumMuteGroups)
	}

	muteGroupsLock.RLock()
	var currentMuted bool
	if muteGroups[groupID-1] != nil {
		currentMuted = muteGroups[groupID-1].Muted
	}
	muteGroupsLock.RUnlock()

	return SetMuteGroupState(groupID, !currentMuted)
}

// UpdateMuteGroupConfig atualiza o nome e a lista de canais (1 a 32) de um Mute Group (exclusivo para ADMIN).
// Caso o grupo esteja ativo (Muted == true) durante a edição:
// - Novos canais adicionados ao grupo são imediatamente mutados (On = 0).
// - Canais removidos do grupo são desmutados (On = 1) caso não pertençam a nenhum outro Mute Group ativo.
func UpdateMuteGroupConfig(groupID int, name string, channels []int) (*MuteGroup, error) {
	if groupID < 1 || groupID > NumMuteGroups {
		return nil, fmt.Errorf("mute group %d inválido (disponíveis: 1 a %d)", groupID, NumMuteGroups)
	}

	cleanChannels, err := normalizeMuteGroupChannels(channels)
	if err != nil {
		return nil, err
	}

	cleanName := strings.TrimSpace(name)
	if cleanName == "" {
		cleanName = fmt.Sprintf("MUTE %d", groupID)
	}
	if len([]rune(cleanName)) > 24 {
		runes := []rune(cleanName)
		cleanName = string(runes[:24])
	}

	muteGroupsLock.Lock()
	if muteGroups[groupID-1] == nil {
		muteGroups[groupID-1] = &MuteGroup{
			ID:       groupID,
			Name:     cleanName,
			Channels: []int{},
		}
	}
	g := muteGroups[groupID-1]

	oldChannels := make(map[int]bool, len(g.Channels))
	for _, ch := range g.Channels {
		oldChannels[ch] = true
	}
	newChannelsMap := make(map[int]bool, len(cleanChannels))
	for _, ch := range cleanChannels {
		newChannelsMap[ch] = true
	}

	g.Name = cleanName
	g.Channels = cleanChannels
	g.UpdatedAt = time.Now()

	var transitions []channelMuteTransition
	if g.Muted {
		// Canais recém-incluídos em um grupo já mutado devem ser mutados imediatamente
		for _, ch := range cleanChannels {
			if !oldChannels[ch] {
				transitions = append(transitions, channelMuteTransition{channel: ch, onValue: 0})
			}
		}
		// Canais removidos do grupo ativo devem ser desmutados se não pertencerem a outro grupo ativo
		for ch := range oldChannels {
			if !newChannelsMap[ch] && !isChannelInAnyActiveGroupLocked(ch, groupID) {
				transitions = append(transitions, channelMuteTransition{channel: ch, onValue: 1})
			}
		}
	}

	if err := saveMuteGroupsLocked(); err != nil {
		muteGroupsLock.Unlock()
		return nil, err
	}
	resultCopy := cloneMuteGroup(g)
	muteGroupsLock.Unlock()

	for _, tr := range transitions {
		emitChannelMuteTransition(tr.channel, tr.onValue)
	}
	broadcastMuteGroupsState()

	return resultCopy, nil
}

// ==========================================
// HTTP HANDLERS (REST API DE MUTE GROUPS)
// ==========================================

// handleAPIMuteGroups atende GET /api/mute-groups
func handleAPIMuteGroups(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sess := getSessionFromRequest(r)
	if sess == nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return
	}

	if strings.EqualFold(sess.User.Role, "PERFORMER") {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Acesso negado para perfil PERFORMER"})
		return
	}

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Método não permitido"})
		return
	}

	state := GetCurrentMixerState()
	channelNames := make([]string, 32)
	for i := 0; i < 32; i++ {
		channelNames[i] = state.Inputs[i].Name
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       true,
		"groups":        ListMuteGroups(),
		"channel_names": channelNames,
	})
}

// handleAPIMuteGroupDetail atende:
// - PUT /api/mute-groups/{id}         (Configuração de nome e canais — Exclusivo ADMIN)
// - POST /api/mute-groups/{id}/toggle (Acionamento mutar/desmutar — ADMIN, OPERATOR e BASIC)
func handleAPIMuteGroupDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sess := getSessionFromRequest(r)
	if sess == nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return
	}

	if strings.EqualFold(sess.User.Role, "PERFORMER") {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Acesso negado para perfil PERFORMER"})
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "ID do Mute Group não informado"})
		return
	}

	groupID, err := strconv.Atoi(parts[2])
	if err != nil || groupID < 1 || groupID > NumMuteGroups {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("ID de Mute Group inválido (use 1 a %d)", NumMuteGroups),
		})
		return
	}

	// Sub-rota: POST /api/mute-groups/{id}/toggle
	if len(parts) >= 4 && parts[3] == "toggle" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Use POST"})
			return
		}

		// Verifica se foi enviado um estado explícito {"muted": bool} ou apenas toggle
		var optReq struct {
			Muted *bool `json:"muted,omitempty"`
		}
		_ = json.NewDecoder(r.Body).Decode(&optReq)

		var updated *MuteGroup
		if optReq.Muted != nil {
			updated, _, err = SetMuteGroupState(groupID, *optReq.Muted)
		} else {
			updated, _, err = ToggleMuteGroup(groupID)
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"group":   updated,
			"groups":  ListMuteGroups(),
		})
		return
	}

	switch r.Method {
	case http.MethodGet:
		g, err := GetMuteGroup(groupID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"group":   g,
		})

	case http.MethodPut, http.MethodPost:
		// Configuração de canais e nome do Mute Group é estritamente exclusiva para ADMIN
		if requireAdminRole(w, r) == nil {
			return
		}

		var req struct {
			Name     string `json:"name"`
			Channels []int  `json:"channels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Requisição JSON inválida"})
			return
		}

		updated, err := UpdateMuteGroupConfig(groupID, req.Name, req.Channels)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"group":   updated,
			"groups":  ListMuteGroups(),
			"message": fmt.Sprintf("Mute Group '%s' atualizado com sucesso (%d canais)!", updated.Name, len(updated.Channels)),
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Método não permitido"})
	}
}
