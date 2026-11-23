package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ==========================================
// MODELO DE DADOS: MIXES PESSOAIS (PERFORMER)
// ==========================================

// PersonalMixGroups armazena os canais (1 a 32) de cada um dos 3 eixos da mix pessoal.
// Regra: dentro de uma mesma Mix, um canal pertence a no máximo um grupo.
type PersonalMixGroups struct {
	Me     []int `json:"me"`     // Canais do grupo "Eu" (1 a 32)
	Voices []int `json:"voices"` // Canais do grupo "Vozes" (1 a 32)
	Others []int `json:"others"` // Canais do grupo "Outros" (1 a 32)
}

// PersonalMix representa a definição completa de uma mix pessoal criada pelo Admin.
type PersonalMix struct {
	ID                string            `json:"id"`                 // Identificador único (slug)
	Name              string            `json:"name"`               // Nome amigável da mix (ex: "Mix Vocal Principal")
	Description       string            `json:"description"`        // Contexto / observações
	OutputAux         int               `json:"output_aux"`         // Canal de saída auxiliar (1 a 8 -> AUX 1 a AUX 8)
	Enabled           bool              `json:"enabled"`            // Se a mix está habilitada para uso pelo PERFORMER
	PerformerUsername string            `json:"performer_username"` // Username do PERFORMER vinculado (máx 1 mix por performer)
	Groups            PersonalMixGroups `json:"groups"`             // Os 3 grupos contextuais (Eu, Vozes, Outros)
	BaseLevels        [32]int           `json:"base_levels"`        // Volumes base de referência (0..1023) configurados pelo Admin no Aux
	JoystickX         float64           `json:"joystick_x"`         // Última posição X do joystick (-1.0 a 1.0)
	JoystickY         float64           `json:"joystick_y"`         // Última posição Y do joystick (-1.0 a 1.0)
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// PerformerUserInfo resume os dados de um usuário PERFORMER para a tela de administração.
type PerformerUserInfo struct {
	Name          string `json:"name"`
	Username      string `json:"username"`
	AssignedMixID string `json:"assigned_mix_id,omitempty"`
}

var (
	personalMixesLock     sync.RWMutex
	personalMixes         = make(map[string]*PersonalMix)
	personalMixesFilePath = filepath.Join("data", "personal_mixes.json")
)

// InitPersonalMixManager configura o caminho de persistência e carrega as mixes salvas do disco.
func InitPersonalMixManager(baseDir string) {
	personalMixesLock.Lock()
	if strings.TrimSpace(baseDir) != "" {
		personalMixesFilePath = filepath.Join(baseDir, "personal_mixes.json")
	} else {
		personalMixesFilePath = filepath.Join("data", "personal_mixes.json")
	}
	personalMixes = make(map[string]*PersonalMix)
	filePath := personalMixesFilePath
	personalMixesLock.Unlock()

	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		log.Printf("Aviso: Falha ao criar diretório para mixes pessoais '%s': %v", filepath.Dir(filePath), err)
	}

	if err := loadPersonalMixesFromDisk(); err != nil {
		log.Printf("Aviso ao carregar mixes pessoais de '%s': %v", filePath, err)
	}
}

func loadPersonalMixesFromDisk() error {
	personalMixesLock.Lock()
	defer personalMixesLock.Unlock()

	data, err := os.ReadFile(personalMixesFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			personalMixes = make(map[string]*PersonalMix)
			return nil
		}
		return err
	}

	var list []*PersonalMix
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("formato inválido em %s: %w", personalMixesFilePath, err)
	}

	personalMixes = make(map[string]*PersonalMix, len(list))
	for _, m := range list {
		if m == nil || strings.TrimSpace(m.ID) == "" {
			continue
		}
		if m.Groups.Me == nil {
			m.Groups.Me = []int{}
		}
		if m.Groups.Voices == nil {
			m.Groups.Voices = []int{}
		}
		if m.Groups.Others == nil {
			m.Groups.Others = []int{}
		}
		personalMixes[m.ID] = m
	}

	log.Printf("Mixes Pessoais: %d mix(es) carregada(s) de '%s'", len(personalMixes), personalMixesFilePath)
	return nil
}

// savePersonalMixesLocked grava atomicamente todas as mixes pessoais em disco.
// Deve ser chamado com personalMixesLock adquirido.
func savePersonalMixesLocked() error {
	list := make([]*PersonalMix, 0, len(personalMixes))
	for _, m := range personalMixes {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].OutputAux != list[j].OutputAux {
			return list[i].OutputAux < list[j].OutputAux
		}
		return list[i].Name < list[j].Name
	})

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("erro ao serializar mixes pessoais: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(personalMixesFilePath), 0755); err != nil {
		return fmt.Errorf("erro ao criar diretório de mixes pessoais: %w", err)
	}

	tmpFile := personalMixesFilePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("erro ao gravar arquivo temporário de mixes: %w", err)
	}
	if err := os.Rename(tmpFile, personalMixesFilePath); err != nil {
		return fmt.Errorf("erro ao atualizar arquivo final de mixes: %w", err)
	}
	return nil
}

// normalizeAndValidateGroups valida canais de 1 a 32, remove duplicatas internas, ordena
// e garante exclusividade mútua entre os 3 grupos ("Eu", "Vozes", "Outros").
func normalizeAndValidateGroups(g PersonalMixGroups) (PersonalMixGroups, error) {
	seen := make(map[int]string)

	cleanGroup := func(channels []int, groupLabel string) ([]int, error) {
		out := make([]int, 0, len(channels))
		localSeen := make(map[int]bool)
		for _, ch := range channels {
			if ch < 1 || ch > 32 {
				return nil, fmt.Errorf("canal %d inválido no grupo '%s' (permitido: 1 a 32)", ch, groupLabel)
			}
			if localSeen[ch] {
				continue
			}
			if prevGroup, exists := seen[ch]; exists {
				return nil, fmt.Errorf("o canal CH %02d não pode pertencer simultaneamente aos grupos '%s' e '%s'", ch, prevGroup, groupLabel)
			}
			localSeen[ch] = true
			seen[ch] = groupLabel
			out = append(out, ch)
		}
		sort.Ints(out)
		return out, nil
	}

	me, err := cleanGroup(g.Me, "Eu")
	if err != nil {
		return PersonalMixGroups{}, err
	}
	voices, err := cleanGroup(g.Voices, "Vozes")
	if err != nil {
		return PersonalMixGroups{}, err
	}
	others, err := cleanGroup(g.Others, "Outros")
	if err != nil {
		return PersonalMixGroups{}, err
	}

	return PersonalMixGroups{
		Me:     me,
		Voices: voices,
		Others: others,
	}, nil
}

// captureBaseLevelsFromCurrentAux copia os níveis de envio atuais do Auxiliar (1..8) da memória da mesa
// como referência base para o multiplicador relativo (Equal Power).
func captureBaseLevelsFromCurrentAux(outputAux int) [32]int {
	var base [32]int
	if outputAux < 1 || outputAux > 8 {
		return base
	}
	state := GetCurrentMixerState()
	auxIdx := outputAux - 1
	for i := 0; i < 32; i++ {
		base[i] = state.Inputs[i].AuxSends[auxIdx].Level
	}
	return base
}

// UpdateBaseLevelFromConsole atualiza o volume base de referência quando o Admin/Operador
// move diretamente um fader de envio auxiliar na superfície principal do console.
func UpdateBaseLevelFromConsole(auxIdx int, chIdx int, level int) {
	if auxIdx < 0 || auxIdx >= 8 || chIdx < 0 || chIdx >= 32 {
		return
	}
	targetAux := auxIdx + 1
	personalMixesLock.Lock()
	defer personalMixesLock.Unlock()

	for _, m := range personalMixes {
		if m.OutputAux == targetAux {
			m.BaseLevels[chIdx] = level
		}
	}
}

// syncPerformerMixInCSVAndSessions atualiza a coluna 'mix' no usuarios.csv e nas sessões ativas
// para manter total consistência com o vínculo configurado na tela Admin.
func syncPerformerMixInCSVAndSessions(performerUsername, mixID string) {
	performerUsername = strings.TrimSpace(performerUsername)
	if performerUsername == "" {
		return
	}

	// 1. Atualiza sessões em memória
	sessionsLock.Lock()
	changedSess := false
	for _, sess := range sessions {
		if sess != nil && strings.EqualFold(sess.User.Username, performerUsername) {
			sess.User.MixID = mixID
			changedSess = true
		}
	}
	if changedSess {
		saveSessionsLocked()
	}
	sessionsLock.Unlock()

	// 2. Atualiza arquivo CSV se possível
	filePath := getUsersCSVFile()
	f, err := os.Open(filePath)
	if err != nil {
		return
	}
	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	f.Close()
	if err != nil || len(records) == 0 {
		return
	}

	updated := false
	for i, rec := range records {
		if len(rec) < 4 {
			continue
		}
		uName := strings.TrimSpace(rec[1])
		role := strings.ToUpper(strings.TrimSpace(rec[3]))
		if i == 0 && (strings.EqualFold(uName, "usuario") || strings.EqualFold(uName, "username")) {
			continue
		}
		if strings.EqualFold(uName, performerUsername) && role == "PERFORMER" {
			for len(rec) < 5 {
				rec = append(rec, "")
			}
			rec[4] = mixID
			records[i] = rec
			updated = true
		}
	}

	if updated {
		var buf strings.Builder
		writer := csv.NewWriter(&buf)
		_ = writer.WriteAll(records)
		writer.Flush()
		tmpFile := filePath + ".tmp"
		if err := os.WriteFile(tmpFile, []byte(buf.String()), 0644); err == nil {
			_ = os.Rename(tmpFile, filePath)
		}
	}
}

// ListPersonalMixes retorna todas as mixes ordenadas por canal de saída Aux e nome.
func ListPersonalMixes() []*PersonalMix {
	personalMixesLock.RLock()
	defer personalMixesLock.RUnlock()

	list := make([]*PersonalMix, 0, len(personalMixes))
	for _, m := range personalMixes {
		copyMix := *m
		copyMix.Groups.Me = append([]int{}, m.Groups.Me...)
		copyMix.Groups.Voices = append([]int{}, m.Groups.Voices...)
		copyMix.Groups.Others = append([]int{}, m.Groups.Others...)
		list = append(list, &copyMix)
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].OutputAux != list[j].OutputAux {
			return list[i].OutputAux < list[j].OutputAux
		}
		return list[i].Name < list[j].Name
	})
	return list
}

// GetPersonalMix busca uma mix pelo seu ID.
func GetPersonalMix(id string) (*PersonalMix, error) {
	cleanID := sanitizeSlug(id)
	personalMixesLock.RLock()
	defer personalMixesLock.RUnlock()

	m, ok := personalMixes[cleanID]
	if !ok {
		return nil, fmt.Errorf("mix pessoal '%s' não encontrada", id)
	}
	copyMix := *m
	copyMix.Groups.Me = append([]int{}, m.Groups.Me...)
	copyMix.Groups.Voices = append([]int{}, m.Groups.Voices...)
	copyMix.Groups.Others = append([]int{}, m.Groups.Others...)
	return &copyMix, nil
}

// GetPersonalMixForPerformer retorna a mix vinculada a um usuário PERFORMER.
func GetPersonalMixForPerformer(username string) *PersonalMix {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil
	}
	personalMixesLock.RLock()
	defer personalMixesLock.RUnlock()

	for _, m := range personalMixes {
		if strings.EqualFold(m.PerformerUsername, username) {
			copyMix := *m
			copyMix.Groups.Me = append([]int{}, m.Groups.Me...)
			copyMix.Groups.Voices = append([]int{}, m.Groups.Voices...)
			copyMix.Groups.Others = append([]int{}, m.Groups.Others...)
			return &copyMix
		}
	}
	return nil
}

// SavePersonalMix cria ou atualiza uma mix pessoal garantindo todas as regras de negócio:
// - Saída Auxiliar entre 1 e 8
// - Canais exclusivos entre os 3 grupos ("Eu", "Vozes", "Outros")
// - Um usuário PERFORMER pode ter no máximo 1 mix vinculada
func SavePersonalMix(existingID string, input PersonalMix) (*PersonalMix, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, fmt.Errorf("o nome da mix não pode ser vazio")
	}

	if input.OutputAux < 1 || input.OutputAux > 8 {
		return nil, fmt.Errorf("canal de saída inválido (escolha entre AUX 1 e AUX 8)")
	}

	cleanGroups, err := normalizeAndValidateGroups(input.Groups)
	if err != nil {
		return nil, err
	}

	performerUser := strings.TrimSpace(input.PerformerUsername)
	if performerUser != "" {
		users, err := loadUsers()
		if err != nil {
			return nil, fmt.Errorf("erro ao validar usuários cadastrados: %w", err)
		}
		foundPerformer := false
		for _, u := range users {
			if strings.EqualFold(u.Username, performerUser) {
				if !strings.EqualFold(u.Role, "PERFORMER") {
					return nil, fmt.Errorf("o usuário '%s' possui papel '%s' (apenas usuários PERFORMER podem ser vinculados a uma mix pessoal)", u.Username, u.Role)
				}
				performerUser = u.Username // mantém grafia exata
				foundPerformer = true
				break
			}
		}
		if !foundPerformer {
			return nil, fmt.Errorf("usuário PERFORMER '%s' não encontrado em usuarios.csv", performerUser)
		}
	}

	personalMixesLock.Lock()

	targetID := strings.TrimSpace(existingID)
	if targetID == "" {
		targetID = strings.TrimSpace(input.ID)
	}
	if targetID == "" {
		targetID = sanitizeSlug(name)
	} else {
		targetID = sanitizeSlug(targetID)
	}

	now := time.Now()
	createdAt := now
	var baseLevels [32]int
	var joyX, joyY float64
	var previousPerformer string

	if existing, exists := personalMixes[targetID]; exists && existing != nil {
		createdAt = existing.CreatedAt
		baseLevels = existing.BaseLevels
		joyX = existing.JoystickX
		joyY = existing.JoystickY
		previousPerformer = existing.PerformerUsername
		// Se trocou de canal Auxiliar de saída, captura os níveis base do novo Auxiliar
		if existing.OutputAux != input.OutputAux {
			baseLevels = captureBaseLevelsFromCurrentAux(input.OutputAux)
		}
	} else {
		baseLevels = captureBaseLevelsFromCurrentAux(input.OutputAux)
	}

	// Regra: um PERFORMER pode ter apenas UMA mix disponível.
	// Se este performer estiver vinculado a outra mix, desvincula da anterior automaticamente.
	if performerUser != "" {
		for otherID, otherMix := range personalMixes {
			if otherID != targetID && strings.EqualFold(otherMix.PerformerUsername, performerUser) {
				otherMix.PerformerUsername = ""
				otherMix.UpdatedAt = now
			}
		}
	}

	mix := &PersonalMix{
		ID:                targetID,
		Name:              name,
		Description:       strings.TrimSpace(input.Description),
		OutputAux:         input.OutputAux,
		Enabled:           input.Enabled,
		PerformerUsername: performerUser,
		Groups:            cleanGroups,
		BaseLevels:        baseLevels,
		JoystickX:         joyX,
		JoystickY:         joyY,
		CreatedAt:         createdAt,
		UpdatedAt:         now,
	}

	personalMixes[targetID] = mix
	err = savePersonalMixesLocked()
	personalMixesLock.Unlock()

	if err != nil {
		return nil, err
	}

	// Sincroniza vínculo no CSV e nas sessões
	if previousPerformer != "" && !strings.EqualFold(previousPerformer, performerUser) {
		syncPerformerMixInCSVAndSessions(previousPerformer, "")
	}
	if performerUser != "" {
		syncPerformerMixInCSVAndSessions(performerUser, mix.ID)
	}

	return mix, nil
}

// CapturePersonalMixBaseLevels força a atualização dos volumes base de referência a partir do estado atual do Auxiliar na mesa.
func CapturePersonalMixBaseLevels(id string) (*PersonalMix, error) {
	cleanID := sanitizeSlug(id)
	personalMixesLock.Lock()
	mix, exists := personalMixes[cleanID]
	if !exists || mix == nil {
		personalMixesLock.Unlock()
		return nil, fmt.Errorf("mix pessoal '%s' não encontrada", id)
	}

	mix.BaseLevels = captureBaseLevelsFromCurrentAux(mix.OutputAux)
	mix.UpdatedAt = time.Now()
	err := savePersonalMixesLocked()
	copyMix := *mix
	personalMixesLock.Unlock()

	if err != nil {
		return nil, err
	}
	return &copyMix, nil
}

// DeletePersonalMix remove uma mix pessoal do servidor e limpa o vínculo do performer.
func DeletePersonalMix(id string) error {
	cleanID := sanitizeSlug(id)
	personalMixesLock.Lock()
	existing, exists := personalMixes[cleanID]
	if !exists {
		personalMixesLock.Unlock()
		return fmt.Errorf("mix pessoal '%s' não existe", id)
	}
	prevPerformer := existing.PerformerUsername
	delete(personalMixes, cleanID)
	err := savePersonalMixesLocked()
	personalMixesLock.Unlock()

	if err != nil {
		return err
	}
	if prevPerformer != "" {
		syncPerformerMixInCSVAndSessions(prevPerformer, "")
	}
	return nil
}

// ==========================================
// CÁLCULO EQUAL POWER (JOYSTICK 3 EIXOS)
// ==========================================

// CalculateEqualPowerWeights recebe as coordenadas (x, y) do disco/joystick (-1..1, +y para cima em direção a "Eu")
// e retorna os pesos de amplitude por Equal Power (wMe² + wVoices² + wOthers² = 1), além das coordenadas
// limitadas ao círculo unitário (clampedX, clampedY).
//
// Eixos no círculo unitário (120° entre si):
// - EU:     90°  -> (0, +1)
// - VOZES:  210° -> (-√3/2, -0.5)
// - OUTROS: 330° -> (+√3/2, -0.5)
func CalculateEqualPowerWeights(x, y float64) (wMe, wVoices, wOthers, clampedX, clampedY float64) {
	clampedX, clampedY = x, y
	r := math.Hypot(clampedX, clampedY)
	if r > 1.0 {
		clampedX /= r
		clampedY /= r
	}

	const sqrt3Over2 = 0.8660254037844386 // √3 / 2

	dotMe := clampedY
	dotVoices := -sqrt3Over2*clampedX - 0.5*clampedY
	dotOthers := +sqrt3Over2*clampedX - 0.5*clampedY

	aMe := math.Max(0, 1.0+2.0*dotMe)
	aVoices := math.Max(0, 1.0+2.0*dotVoices)
	aOthers := math.Max(0, 1.0+2.0*dotOthers)

	sumPower := aMe + aVoices + aOthers
	if sumPower <= 1e-9 {
		// Fallback neutro no centro (1/√3 em cada eixo)
		eq := math.Sqrt(1.0 / 3.0)
		return eq, eq, eq, clampedX, clampedY
	}

	pMe := aMe / sumPower
	pVoices := aVoices / sumPower
	pOthers := aOthers / sumPower

	wMe = math.Sqrt(pMe)
	wVoices = math.Sqrt(pVoices)
	wOthers = math.Sqrt(pOthers)
	return
}

// emitPerformerAuxSend aplica o nível calculado ao espelho da mesa e envia via throttler para MIDI + WebSocket,
// sem sobrescrever o BaseLevels configurado pelo Admin.
func emitPerformerAuxSend(auxIdx int, chIdx int, value int) {
	if auxIdx < 0 || auxIdx >= 8 || chIdx < 0 || chIdx >= 32 {
		return
	}
	if value < 0 {
		value = 0
	} else if value > 1023 {
		value = 1023
	}

	mixerStateLock.Lock()
	currentMixer.Inputs[chIdx].AuxSends[auxIdx].Level = value
	stateDirty = true
	mixerStateLock.Unlock()

	elem := byte(0x23)
	param := byte(auxIdx*3 + 2)
	ch := byte(chIdx)

	sysex := Build01V96SysEx(elem, param, ch, value)
	hexStr := fmt.Sprintf("% X", sysex)

	canMIDI, canWS := throttler.allowOrSchedule(elem, param, ch, value, func(finalVal int) {
		finalSysex := Build01V96SysEx(elem, param, ch, finalVal)
		finalHex := fmt.Sprintf("% X", finalSysex)

		markSentEcho(elem, param, ch, finalVal)

		midiLock.Lock()
		if err := sendMIDISysExLocked(finalSysex); err != nil {
			log.Printf("Erro ao enviar MIDI (flush performer mix): %v", err)
		}
		midiLock.Unlock()

		broadcastWS(WSMessage{
			Type:    "param",
			Element: elem,
			Param:   param,
			Channel: ch,
			Value:   finalVal,
			Hex:     finalHex,
		})
	})

	if canMIDI {
		markSentEcho(elem, param, ch, value)
		midiLock.Lock()
		if err := sendMIDISysExLocked(sysex); err != nil {
			log.Printf("Erro ao enviar MIDI (performer mix): %v", err)
		}
		midiLock.Unlock()
	}

	if canWS {
		broadcastWS(WSMessage{
			Type:    "param",
			Element: elem,
			Param:   param,
			Channel: ch,
			Value:   value,
			Hex:     hexStr,
		})
	}
}

// ApplyPersonalMixJoystick calcula por Equal Power os novos volumes de cada grupo ("Eu", "Vozes", "Outros")
// como multiplicador relativo sobre o BaseLevel do canal no Auxiliar da Mix e emite os novos volumes.
func ApplyPersonalMixJoystick(mixID string, x, y float64, persistDisk bool) (*PersonalMix, map[int]int, [3]float64, error) {
	cleanID := sanitizeSlug(mixID)
	wMe, wVoices, wOthers, cx, cy := CalculateEqualPowerWeights(x, y)
	weights := [3]float64{wMe, wVoices, wOthers}

	personalMixesLock.Lock()
	mix, exists := personalMixes[cleanID]
	if !exists || mix == nil {
		personalMixesLock.Unlock()
		return nil, nil, weights, fmt.Errorf("mix pessoal '%s' não encontrada", mixID)
	}
	if !mix.Enabled {
		personalMixesLock.Unlock()
		return nil, nil, weights, fmt.Errorf("esta mix pessoal está desabilitada pelo administrador")
	}

	auxIdx := mix.OutputAux - 1
	if auxIdx < 0 || auxIdx >= 8 {
		personalMixesLock.Unlock()
		return nil, nil, weights, fmt.Errorf("auxiliar de saída inválido na mix")
	}

	// Se algum canal do grupo estiver com BaseLevel == 0 mas o estado atual na memória tiver valor > 0
	// (ex: Admin subiu fader antes sem clicar em Capturar Base), adota o valor atual como referência inicial.
	state := GetCurrentMixerState()
	ensureBase := func(channels []int) {
		for _, ch := range channels {
			if ch >= 1 && ch <= 32 {
				idx := ch - 1
				if mix.BaseLevels[idx] == 0 && state.Inputs[idx].AuxSends[auxIdx].Level > 0 {
					mix.BaseLevels[idx] = state.Inputs[idx].AuxSends[auxIdx].Level
				}
			}
		}
	}
	ensureBase(mix.Groups.Me)
	ensureBase(mix.Groups.Voices)
	ensureBase(mix.Groups.Others)

	mix.JoystickX = cx
	mix.JoystickY = cy
	mix.UpdatedAt = time.Now()

	if persistDisk {
		_ = savePersonalMixesLocked()
	}

	copyMix := *mix
	copyMix.Groups.Me = append([]int{}, mix.Groups.Me...)
	copyMix.Groups.Voices = append([]int{}, mix.Groups.Voices...)
	copyMix.Groups.Others = append([]int{}, mix.Groups.Others...)
	personalMixesLock.Unlock()

	emittedVolumes := make(map[int]int)

	applyGroup := func(channels []int, weight float64) {
		for _, ch := range channels {
			if ch < 1 || ch > 32 {
				continue
			}
			chIdx := ch - 1
			baseVal := copyMix.BaseLevels[chIdx]
			newVal := int(math.Round(float64(baseVal) * weight))
			if newVal < 0 {
				newVal = 0
			} else if newVal > 1023 {
				newVal = 1023
			}
			emittedVolumes[ch] = newVal
			emitPerformerAuxSend(auxIdx, chIdx, newVal)
		}
	}

	applyGroup(copyMix.Groups.Me, wMe)
	applyGroup(copyMix.Groups.Voices, wVoices)
	applyGroup(copyMix.Groups.Others, wOthers)

	return &copyMix, emittedVolumes, weights, nil
}

// listPerformerUsers retorna todos os usuários com papel PERFORMER e qual mix está atribuída a cada um.
func listPerformerUsers() []PerformerUserInfo {
	users, err := loadUsers()
	if err != nil {
		return []PerformerUserInfo{}
	}

	personalMixesLock.RLock()
	assignedByUsername := make(map[string]string)
	for _, m := range personalMixes {
		if m.PerformerUsername != "" {
			assignedByUsername[strings.ToLower(m.PerformerUsername)] = m.ID
		}
	}
	personalMixesLock.RUnlock()

	var performers []PerformerUserInfo
	for _, u := range users {
		if strings.EqualFold(u.Role, "PERFORMER") {
			assigned := assignedByUsername[strings.ToLower(u.Username)]
			performers = append(performers, PerformerUserInfo{
				Name:          u.Name,
				Username:      u.Username,
				AssignedMixID: assigned,
			})
		}
	}
	return performers
}

// ==========================================
// HTTP HANDLERS (REST API DE MIXES PESSOAIS)
// ==========================================

func handleAPIPersonalMixes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sess := getSessionFromRequest(r)
	if sess == nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		state := GetCurrentMixerState()
		channelNames := make([]string, 32)
		for i := 0; i < 32; i++ {
			channelNames[i] = state.Inputs[i].Name
		}
		auxNames := make([]string, 8)
		for i := 0; i < 8; i++ {
			auxNames[i] = state.AuxMasters[i].Name
		}

		// Se for PERFORMER, retorna apenas a sua própria mix vinculada
		if strings.EqualFold(sess.User.Role, "PERFORMER") {
			myMix := GetPersonalMixForPerformer(sess.User.Username)
			if myMix == nil && sess.User.MixID != "" {
				myMix, _ = GetPersonalMix(sess.User.MixID)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success":       true,
				"mix":           myMix,
				"channel_names": channelNames,
				"aux_names":     auxNames,
			})
			return
		}

		// Para ADMIN / OPERATOR, retorna a lista completa de mixes e performers disponíveis
		mixes := ListPersonalMixes()
		performers := listPerformerUsers()

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":       true,
			"mixes":         mixes,
			"performers":    performers,
			"channel_names": channelNames,
			"aux_names":     auxNames,
		})

	case http.MethodPost:
		if requireAdminRole(w, r) == nil {
			return
		}

		var req PersonalMix
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Requisição JSON inválida"})
			return
		}

		saved, err := SavePersonalMix("", req)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"mix":     saved,
			"message": fmt.Sprintf("Mix pessoal '%s' salva com sucesso!", saved.Name),
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Método não permitido"})
	}
}

func handleAPIPersonalMixDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sess := getSessionFromRequest(r)
	if sess == nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return
	}

	// Extrai ID da rota: /api/personal-mixes/{id} ou /api/personal-mixes/{id}/capture-base
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || strings.TrimSpace(parts[2]) == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "ID da mix pessoal não especificado"})
		return
	}
	mixID := parts[2]

	// Sub-rota: POST /api/personal-mixes/{id}/capture-base
	if len(parts) >= 4 && parts[3] == "capture-base" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Use POST"})
			return
		}
		if requireAdminRole(w, r) == nil {
			return
		}
		updated, err := CapturePersonalMixBaseLevels(mixID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"mix":     updated,
			"message": fmt.Sprintf("Volumes base do AUX %d capturados com sucesso para a mix '%s'!", updated.OutputAux, updated.Name),
		})
		return
	}

	// Sub-rota: POST /api/personal-mixes/{id}/joystick
	if len(parts) >= 4 && parts[3] == "joystick" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Use POST"})
			return
		}

		targetMixID := sanitizeSlug(mixID)
		if strings.EqualFold(sess.User.Role, "PERFORMER") {
			myMix := GetPersonalMixForPerformer(sess.User.Username)
			if myMix == nil && sess.User.MixID != "" {
				myMix, _ = GetPersonalMix(sess.User.MixID)
			}
			if myMix == nil || myMix.ID != targetMixID {
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Você não tem permissão para controlar esta mix pessoal"})
				return
			}
		}

		var req struct {
			X       float64 `json:"x"`
			Y       float64 `json:"y"`
			Persist bool    `json:"persist"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "JSON inválido"})
			return
		}

		updatedMix, volumes, weights, err := ApplyPersonalMixJoystick(targetMixID, req.X, req.Y, req.Persist)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"mix":     updatedMix,
			"volumes": volumes,
			"weights": map[string]float64{
				"me":     weights[0],
				"voices": weights[1],
				"others": weights[2],
			},
		})
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Se for PERFORMER, verifica se esta mix pertence a ele
		if strings.EqualFold(sess.User.Role, "PERFORMER") {
			myMix := GetPersonalMixForPerformer(sess.User.Username)
			if myMix == nil || myMix.ID != sanitizeSlug(mixID) {
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Você não tem permissão para acessar esta mix pessoal"})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"mix":     myMix,
			})
			return
		}

		mix, err := GetPersonalMix(mixID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"mix":     mix,
		})

	case http.MethodPut:
		if requireAdminRole(w, r) == nil {
			return
		}
		var req PersonalMix
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Requisição JSON inválida"})
			return
		}
		saved, err := SavePersonalMix(mixID, req)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"mix":     saved,
			"message": fmt.Sprintf("Mix pessoal '%s' atualizada com sucesso!", saved.Name),
		})

	case http.MethodDelete:
		if requireAdminRole(w, r) == nil {
			return
		}
		if err := DeletePersonalMix(mixID); err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Mix pessoal excluída com sucesso",
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Método não permitido"})
	}
}
