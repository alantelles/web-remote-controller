package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ==========================================
// ESTRUTURAS DE DADOS DO ESTADO E DAS CENAS
// ==========================================

// EQBand representa uma banda de equalização paramétrica
type EQBand struct {
	Q    int `json:"q"`    // Q ou Tipo de Filtro (0x01, 0x04, 0x07, 0x0A)
	Freq int `json:"freq"` // Frequência 0 a 138 (20Hz a 20kHz)
	Gain int `json:"gain"` // Ganho 0 a 72 (36 = 0.0 dB)
}

// EQState representa o equalizador de 4 bandas da Yamaha 01V96
type EQState struct {
	On     int    `json:"on"`     // 0 = Bypass, 1 = On (0x00)
	Low    EQBand `json:"low"`    // Banda LOW
	LowMid EQBand `json:"low_mid"`// Banda LO-MID
	HiMid  EQBand `json:"hi_mid"` // Banda HI-MID
	High   EQBand `json:"high"`   // Banda HIGH
	HPFOn  int    `json:"hpf_on"` // 1 = HPF ativo (0x0D)
	LPFOn  int    `json:"lpf_on"` // 1 = LPF ativo (0x0E)
}

// CompState representa o compressor de canal ou saída
type CompState struct {
	On        int `json:"on"`        // 0 = Bypass, 1 = On (0x00)
	Threshold int `json:"threshold"` // 0 (-54dB) a 54 (0dB) (0x01)
	Ratio     int `json:"ratio"`     // 0 a 15 (0x02)
	Attack    int `json:"attack"`    // 0 a 120 ms (0x03)
	Release   int `json:"release"`   // 0 a 79 (0x04)
	Knee      int `json:"knee"`      // 0 (hard) a 5 (soft) (0x05)
	Gain      int `json:"gain"`      // 0 a 36 (0dB a +18dB) (0x06)
}

// AuxSend representa o envio de um canal para um auxiliar
type AuxSend struct {
	Aux   int `json:"aux"`   // 1 a 8
	Level int `json:"level"` // 0 a 1023 (823 = 0.0 dB)
	On    int `json:"on"`    // 1 = ON, 0 = MUTE
	Pre   int `json:"pre"`   // 0 = Post, 1 = Pre
}

// InputChannel representa o estado completo de um canal de entrada (1 a 32)
type InputChannel struct {
	Channel  int        `json:"channel"`   // 1 a 32
	Name     string     `json:"name"`      // Rótulo amigável ("Vocal 1", "Bumbo")
	Fader    int        `json:"fader"`     // 0 a 1023 (823 = 0.0 dB)
	On       int        `json:"on"`        // 1 = ON, 0 = MUTE
	Pan      int        `json:"pan"`       // 0 a 126 (63 = Center)
	AuxSends [8]AuxSend `json:"aux_sends"` // Envios para Aux 1 a 8
	EQ       EQState    `json:"eq"`        // Equalizador 4 bandas
	Comp     CompState  `json:"comp"`      // Compressor
}

// AuxMasterState representa o estado de uma saída master auxiliar (1 a 8)
type AuxMasterState struct {
	Aux   int       `json:"aux"`   // 1 a 8
	Name  string    `json:"name"`  // Nome do Aux ("Monitor 1", etc.)
	Fader int       `json:"fader"` // 0 a 1023 (823 = 0.0 dB)
	On    int       `json:"on"`    // 1 = ON, 0 = MUTE
	EQ    EQState   `json:"eq"`    // Equalizador 4 bandas
	Comp  CompState `json:"comp"`  // Compressor
}

// StereoMasterState representa a saída master Stereo principal
type StereoMasterState struct {
	Fader int       `json:"fader"` // 0 a 1023 (823 = 0.0 dB)
	On    int       `json:"on"`    // 1 = ON, 0 = MUTE
	EQ    EQState   `json:"eq"`    // Equalizador 4 bandas
	Comp  CompState `json:"comp"`  // Compressor
}

// MixerState é o snapshot completo de todos os parâmetros gerenciados da 01V96
type MixerState struct {
	StereoMaster StereoMasterState `json:"stereo_master"`
	AuxMasters   [8]AuxMasterState `json:"aux_masters"`
	Inputs       [32]InputChannel  `json:"inputs"`
}

// Scene encapsula o snapshot de estado junto com metadados administrativos
type Scene struct {
	Version     int        `json:"version"`     // 1
	ID          string     `json:"id"`          // slug único e seguro para arquivo
	Name        string     `json:"name"`        // Nome amigável ("Culto Domingo")
	Description string     `json:"description"` // Comentários adicionais
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Author      string     `json:"author"`      // Usuário que criou/salvou a cena
	State       MixerState `json:"state"`
}

// SceneMetadata é um resumo leve para listagem de cenas na interface
type SceneMetadata struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Author      string    `json:"author"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	FileSize    int64     `json:"file_size"`
}

// ==========================================
// GERENCIADOR DE ESTADO E CENAS EM MEMÓRIA
// ==========================================

var (
	mixerStateLock sync.RWMutex
	currentMixer   MixerState

	stateDirty        bool
	lastAutoSaveTime  time.Time
	autoSaveLock      sync.Mutex
	dataDirRoot       = "data"
	scenesDirRoot     = filepath.Join("data", "scenes")
	lastStateFilePath = filepath.Join("data", "last_state.json")
	autoSaveTicker    *time.Ticker
	autoSaveStopChan  chan struct{}
)

// newDefaultEQState cria um estado seguro de EQ plano (Flat)
func newDefaultEQState() EQState {
	return EQState{
		On:     0,
		Low:    EQBand{Q: 0, Freq: 46, Gain: 36},
		LowMid: EQBand{Q: 15, Freq: 70, Gain: 36},
		HiMid:  EQBand{Q: 15, Freq: 95, Gain: 36},
		High:   EQBand{Q: 0, Freq: 115, Gain: 36},
		HPFOn:  0,
		LPFOn:  0,
	}
}

// newDefaultCompState cria um estado neutro e seguro de Compressor
func newDefaultCompState() CompState {
	return CompState{
		On:        0,
		Threshold: 30, // -24 dB
		Ratio:     5,  // 2.0:1
		Attack:    20, // 20 ms
		Release:   40,
		Knee:      1,
		Gain:      0, // 0 dB
	}
}

// NewDefaultMixerState inicializa uma estrutura completa com valores padrão seguros
func NewDefaultMixerState() MixerState {
	var state MixerState

	// Stereo Master
	state.StereoMaster = StereoMasterState{
		Fader: 823, // 0.0 dB
		On:    1,
		EQ:    newDefaultEQState(),
		Comp:  newDefaultCompState(),
	}

	// Aux Masters 1 a 8
	for i := 0; i < 8; i++ {
		state.AuxMasters[i] = AuxMasterState{
			Aux:   i + 1,
			Name:  fmt.Sprintf("AUX %d", i+1),
			Fader: 823, // 0.0 dB
			On:    1,
			EQ:    newDefaultEQState(),
			Comp:  newDefaultCompState(),
		}
	}

	// Inputs 1 a 32
	for i := 0; i < 32; i++ {
		ch := InputChannel{
			Channel: i + 1,
			Name:    fmt.Sprintf("CH %02d", i+1),
			Fader:   0, // -infinito
			On:      1, // ON ativo
			Pan:     63, // Centro
			EQ:      newDefaultEQState(),
			Comp:    newDefaultCompState(),
		}
		for a := 0; a < 8; a++ {
			ch.AuxSends[a] = AuxSend{
				Aux:   a + 1,
				Level: 0, // -infinito
				On:    1,
				Pre:   0, // Post-fader
			}
		}
		state.Inputs[i] = ch
	}

	return state
}

// ApplyParamToState atualiza o espelho de memória com base nos bytes de parâmetros da 01V96
func ApplyParamToState(element, param, channel byte, value int) {
	mixerStateLock.Lock()
	defer mixerStateLock.Unlock()

	stateDirty = true

	chIdx := int(channel)

	switch element {
	// Input Fader (0x1C)
	case 0x1C:
		if chIdx >= 0 && chIdx < 32 {
			currentMixer.Inputs[chIdx].Fader = value
		}

	// Input ON / MUTE (0x1A)
	case 0x1A:
		if chIdx >= 0 && chIdx < 32 {
			currentMixer.Inputs[chIdx].On = value
		}

	// Input PAN (0x1B)
	case 0x1B:
		if chIdx >= 0 && chIdx < 32 {
			currentMixer.Inputs[chIdx].Pan = value
		}

	// Input Aux Sends (0x23)
	case 0x23:
		if chIdx >= 0 && chIdx < 32 {
			if param <= 0x07 {
				auxIdx := int(param)
				currentMixer.Inputs[chIdx].AuxSends[auxIdx].Level = value
			} else if param >= 0x08 && param <= 0x0F {
				auxIdx := int(param - 0x08)
				currentMixer.Inputs[chIdx].AuxSends[auxIdx].On = value
			} else if param >= 0x10 && param <= 0x17 {
				auxIdx := int(param - 0x10)
				currentMixer.Inputs[chIdx].AuxSends[auxIdx].Pre = value
			}
		}

	// Input EQ (0x20)
	case 0x20:
		if chIdx >= 0 && chIdx < 32 {
			applyEQParam(&currentMixer.Inputs[chIdx].EQ, param, value)
		}

	// Input Compressor (0x22)
	case 0x22:
		if chIdx >= 0 && chIdx < 32 {
			applyCompParam(&currentMixer.Inputs[chIdx].Comp, param, value)
		}

	// Stereo Master Fader (0x4D)
	case 0x4D:
		currentMixer.StereoMaster.Fader = value

	// Stereo Master ON / MUTE (0x4B)
	case 0x4B:
		currentMixer.StereoMaster.On = value

	// Stereo Master EQ (0x50)
	case 0x50:
		applyEQParam(&currentMixer.StereoMaster.EQ, param, value)

	// Stereo Master COMP (0x51)
	case 0x51:
		applyCompParam(&currentMixer.StereoMaster.Comp, param, value)

	// Aux Master Fader (0x31)
	case 0x31:
		if chIdx >= 0 && chIdx < 8 {
			currentMixer.AuxMasters[chIdx].Fader = value
		}

	// Aux Master ON / MUTE (0x2F)
	case 0x2F:
		if chIdx >= 0 && chIdx < 8 {
			currentMixer.AuxMasters[chIdx].On = value
		}

	// Aux Master EQ (0x35)
	case 0x35:
		if chIdx >= 0 && chIdx < 8 {
			applyEQParam(&currentMixer.AuxMasters[chIdx].EQ, param, value)
		}

	// Aux Master COMP (0x36)
	case 0x36:
		if chIdx >= 0 && chIdx < 8 {
			applyCompParam(&currentMixer.AuxMasters[chIdx].Comp, param, value)
		}
	}
}

func applyEQParam(eq *EQState, param byte, value int) {
	switch param {
	case 0x00:
		eq.On = value
	case 0x01:
		eq.Low.Q = value
	case 0x02:
		eq.Low.Freq = value
	case 0x03:
		eq.Low.Gain = value
	case 0x04:
		eq.LowMid.Q = value
	case 0x05:
		eq.LowMid.Freq = value
	case 0x06:
		eq.LowMid.Gain = value
	case 0x07:
		eq.HiMid.Q = value
	case 0x08:
		eq.HiMid.Freq = value
	case 0x09:
		eq.HiMid.Gain = value
	case 0x0A:
		eq.High.Q = value
	case 0x0B:
		eq.High.Freq = value
	case 0x0C:
		eq.High.Gain = value
	case 0x0D:
		eq.HPFOn = value
	case 0x0E:
		eq.LPFOn = value
	}
}

func applyCompParam(comp *CompState, param byte, value int) {
	switch param {
	case 0x00:
		comp.On = value
	case 0x01:
		comp.Threshold = value
	case 0x02:
		comp.Ratio = value
	case 0x03:
		comp.Attack = value
	case 0x04:
		comp.Release = value
	case 0x05:
		comp.Knee = value
	case 0x06:
		comp.Gain = value
	}
}

// GetCurrentMixerState retorna uma cópia thread-safe do estado atual
func GetCurrentMixerState() MixerState {
	mixerStateLock.RLock()
	defer mixerStateLock.RUnlock()
	return currentMixer
}

// SetCurrentMixerState atualiza o estado em memória (usado no recall e restauração)
func SetCurrentMixerState(state MixerState) {
	mixerStateLock.Lock()
	defer mixerStateLock.Unlock()
	currentMixer = state
	stateDirty = true
}

// SetChannelCustomName atualiza o rótulo do canal
func SetChannelCustomName(channelNum int, name string) {
	mixerStateLock.Lock()
	defer mixerStateLock.Unlock()
	if channelNum >= 1 && channelNum <= 32 {
		currentMixer.Inputs[channelNum-1].Name = strings.TrimSpace(name)
		stateDirty = true
	}
}

// SetAuxMasterCustomName atualiza o rótulo do mestre auxiliar
func SetAuxMasterCustomName(auxNum int, name string) {
	mixerStateLock.Lock()
	defer mixerStateLock.Unlock()
	if auxNum >= 1 && auxNum <= 8 {
		currentMixer.AuxMasters[auxNum-1].Name = strings.TrimSpace(name)
		stateDirty = true
	}
}

// ==========================================
// PERSISTÊNCIA EM ARQUIVOS (LAST_STATE & CENAS)
// ==========================================

func ensureDataDirectories() {
	if err := os.MkdirAll(scenesDirRoot, 0755); err != nil {
		log.Printf("Aviso: Falha ao criar diretório '%s': %v", scenesDirRoot, err)
	}
}

// InitSceneManager inicializa o gerenciador, lê last_state.json se existir e liga o auto-save
func InitSceneManager(baseDir string) {
	if strings.TrimSpace(baseDir) != "" {
		dataDirRoot = baseDir
		scenesDirRoot = filepath.Join(baseDir, "scenes")
		lastStateFilePath = filepath.Join(baseDir, "last_state.json")
	}

	ensureDataDirectories()

	// Inicia estado com defaults
	SetCurrentMixerState(NewDefaultMixerState())

	// Se existir last_state.json, restaura apenas na memória sem enviar MIDI para a mesa
	if data, err := os.ReadFile(lastStateFilePath); err == nil {
		var savedState MixerState
		if err := json.Unmarshal(data, &savedState); err == nil {
			SetCurrentMixerState(savedState)
			log.Printf("Auto-Save: Último estado conhecido carregado com sucesso de '%s' (Mesa física intocada).", lastStateFilePath)
		} else {
			log.Printf("Aviso: Falha ao decodificar '%s': %v", lastStateFilePath, err)
		}
	} else {
		log.Printf("Auto-Save: Nenhum estado prévio encontrado em '%s'. Iniciado com defaults.", lastStateFilePath)
	}

	// Reseta flag de dirty após carga inicial
	mixerStateLock.Lock()
	stateDirty = false
	mixerStateLock.Unlock()

	startAutoSaveLoop(5 * time.Second)
}

func startAutoSaveLoop(interval time.Duration) {
	autoSaveLock.Lock()
	defer autoSaveLock.Unlock()

	if autoSaveTicker != nil {
		autoSaveTicker.Stop()
	}
	if autoSaveStopChan != nil {
		close(autoSaveStopChan)
		autoSaveStopChan = nil
	}

	ticker := time.NewTicker(interval)
	stopChan := make(chan struct{})
	autoSaveTicker = ticker
	autoSaveStopChan = stopChan

	go func(t *time.Ticker, stop chan struct{}) {
		for {
			select {
			case <-t.C:
				FlushStateToDiskIfDirty()
			case <-stop:
				return
			}
		}
	}(ticker, stopChan)
}

// StopAutoSave encerra o loop de auto-save e faz o flush final
func StopAutoSave() {
	autoSaveLock.Lock()
	if autoSaveTicker != nil {
		autoSaveTicker.Stop()
		autoSaveTicker = nil
	}
	if autoSaveStopChan != nil {
		close(autoSaveStopChan)
		autoSaveStopChan = nil
	}
	autoSaveLock.Unlock()

	FlushStateToDiskIfDirty()
}

// FlushStateToDiskIfDirty salva atomicamente no disco se houver mudanças
func FlushStateToDiskIfDirty() {
	mixerStateLock.Lock()
	if !stateDirty {
		mixerStateLock.Unlock()
		return
	}
	stateDirty = false
	stateCopy := currentMixer
	mixerStateLock.Unlock()

	data, err := json.MarshalIndent(stateCopy, "", "  ")
	if err != nil {
		log.Printf("Erro ao serializar MixerState para auto-save: %v", err)
		return
	}

	tmpFile := lastStateFilePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		log.Printf("Erro ao gravar arquivo temporário de auto-save '%s': %v", tmpFile, err)
		return
	}

	if err := os.Rename(tmpFile, lastStateFilePath); err != nil {
		log.Printf("Erro ao atualizar arquivo final de auto-save '%s': %v", lastStateFilePath, err)
		return
	}

	lastAutoSaveTime = time.Now()
}

// sanitizeSlug gera um ID seguro a partir de um nome de cena
func sanitizeSlug(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	reg := regexp.MustCompile(`[^a-z0-9_-]+`)
	slug = reg.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = fmt.Sprintf("cena-%d", time.Now().Unix())
	}
	return slug
}

// SaveScene grava um arquivo de cena em scenesDirRoot/<id>.json
func SaveScene(name, description, author string) (*Scene, error) {
	ensureDataDirectories()

	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("o nome da cena não pode ser vazio")
	}

	slug := sanitizeSlug(name)
	filePath := filepath.Join(scenesDirRoot, slug+".json")

	now := time.Now()
	createdAt := now

	// Se a cena já existe, preserva o createdAt original
	if existing, err := GetScene(slug); err == nil && existing != nil {
		createdAt = existing.CreatedAt
	}

	scene := &Scene{
		Version:     1,
		ID:          slug,
		Name:        name,
		Description: strings.TrimSpace(description),
		CreatedAt:   createdAt,
		UpdatedAt:   now,
		Author:      author,
		State:       GetCurrentMixerState(),
	}

	data, err := json.MarshalIndent(scene, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("erro ao converter cena para JSON: %w", err)
	}

	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return nil, fmt.Errorf("erro ao salvar arquivo temporário da cena: %w", err)
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		return nil, fmt.Errorf("erro ao salvar arquivo final da cena: %w", err)
	}

	log.Printf("Cena salva com sucesso: '%s' (ID: %s, por %s)", scene.Name, scene.ID, scene.Author)
	return scene, nil
}

// GetScene lê e decodifica uma cena pelo ID
func GetScene(id string) (*Scene, error) {
	cleanID := filepath.Base(sanitizeSlug(id))
	filePath := filepath.Join(scenesDirRoot, cleanID+".json")

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("cena '%s' não encontrada: %w", id, err)
	}

	var scene Scene
	if err := json.Unmarshal(data, &scene); err != nil {
		return nil, fmt.Errorf("falha ao ler formato da cena '%s': %w", id, err)
	}

	return &scene, nil
}

// DeleteScene remove uma cena do disco
func DeleteScene(id string) error {
	cleanID := filepath.Base(sanitizeSlug(id))
	filePath := filepath.Join(scenesDirRoot, cleanID+".json")

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("cena '%s' não existe", id)
	}

	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("falha ao excluir cena: %w", err)
	}

	log.Printf("Cena '%s' removida com sucesso.", cleanID)
	return nil
}

// ListScenes retorna uma lista ordenada de metadados das cenas salvas
func ListScenes() ([]SceneMetadata, error) {
	ensureDataDirectories()

	entries, err := os.ReadDir(scenesDirRoot)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler diretório de cenas: %w", err)
	}

	var list []SceneMetadata
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}

		filePath := filepath.Join(scenesDirRoot, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var s Scene
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}

		info, _ := entry.Info()
		var fileSize int64
		if info != nil {
			fileSize = info.Size()
		}

		list = append(list, SceneMetadata{
			ID:          s.ID,
			Name:        s.Name,
			Description: s.Description,
			Author:      s.Author,
			CreatedAt:   s.CreatedAt,
			UpdatedAt:   s.UpdatedAt,
			FileSize:    fileSize,
		})
	}

	// Ordena por data de atualização (mais recentes primeiro)
	sort.Slice(list, func(i, j int) bool {
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})

	return list, nil
}

// ==========================================
// RECALL SEGURO E CADENCIADO DE CENAS (SYSEX)
// ==========================================

type sysexMessage struct {
	element byte
	param   byte
	channel byte
	value   int
}

// GenerateSysExForState gera a lista completa de comandos SysEx para replicar o estado
func GenerateSysExForState(st *MixerState) []sysexMessage {
	var msgs []sysexMessage

	// 1. STEREO MASTER
	msgs = append(msgs, sysexMessage{element: 0x4D, param: 0x00, channel: 0x00, value: st.StereoMaster.Fader})
	msgs = append(msgs, sysexMessage{element: 0x4B, param: 0x00, channel: 0x00, value: st.StereoMaster.On})
	msgs = append(msgs, generateEQSysEx(0x50, 0x00, &st.StereoMaster.EQ)...)
	msgs = append(msgs, generateCompSysEx(0x51, 0x00, &st.StereoMaster.Comp)...)

	// 2. AUX MASTERS 1 A 8
	for i := 0; i < 8; i++ {
		auxCh := byte(i)
		aux := &st.AuxMasters[i]
		msgs = append(msgs, sysexMessage{element: 0x31, param: 0x00, channel: auxCh, value: aux.Fader})
		msgs = append(msgs, sysexMessage{element: 0x2F, param: 0x00, channel: auxCh, value: aux.On})
		msgs = append(msgs, generateEQSysEx(0x35, auxCh, &aux.EQ)...)
		msgs = append(msgs, generateCompSysEx(0x36, auxCh, &aux.Comp)...)
	}

	// 3. INPUT CHANNELS 1 A 32
	for i := 0; i < 32; i++ {
		ch := byte(i)
		inp := &st.Inputs[i]

		// Fader e On
		msgs = append(msgs, sysexMessage{element: 0x1C, param: 0x00, channel: ch, value: inp.Fader})
		msgs = append(msgs, sysexMessage{element: 0x1A, param: 0x00, channel: ch, value: inp.On})
		msgs = append(msgs, sysexMessage{element: 0x1B, param: 0x00, channel: ch, value: inp.Pan})

		// Aux Sends
		for a := 0; a < 8; a++ {
			send := &inp.AuxSends[a]
			msgs = append(msgs, sysexMessage{element: 0x23, param: byte(a), channel: ch, value: send.Level})
			msgs = append(msgs, sysexMessage{element: 0x23, param: byte(0x08 + a), channel: ch, value: send.On})
			msgs = append(msgs, sysexMessage{element: 0x23, param: byte(0x10 + a), channel: ch, value: send.Pre})
		}

		// EQ e Compressor
		msgs = append(msgs, generateEQSysEx(0x20, ch, &inp.EQ)...)
		msgs = append(msgs, generateCompSysEx(0x22, ch, &inp.Comp)...)
	}

	return msgs
}

func generateEQSysEx(element, channel byte, eq *EQState) []sysexMessage {
	return []sysexMessage{
		{element: element, param: 0x00, channel: channel, value: eq.On},
		{element: element, param: 0x01, channel: channel, value: eq.Low.Q},
		{element: element, param: 0x02, channel: channel, value: eq.Low.Freq},
		{element: element, param: 0x03, channel: channel, value: eq.Low.Gain},
		{element: element, param: 0x04, channel: channel, value: eq.LowMid.Q},
		{element: element, param: 0x05, channel: channel, value: eq.LowMid.Freq},
		{element: element, param: 0x06, channel: channel, value: eq.LowMid.Gain},
		{element: element, param: 0x07, channel: channel, value: eq.HiMid.Q},
		{element: element, param: 0x08, channel: channel, value: eq.HiMid.Freq},
		{element: element, param: 0x09, channel: channel, value: eq.HiMid.Gain},
		{element: element, param: 0x0A, channel: channel, value: eq.High.Q},
		{element: element, param: 0x0B, channel: channel, value: eq.High.Freq},
		{element: element, param: 0x0C, channel: channel, value: eq.High.Gain},
		{element: element, param: 0x0D, channel: channel, value: eq.HPFOn},
		{element: element, param: 0x0E, channel: channel, value: eq.LPFOn},
	}
}

func generateCompSysEx(element, channel byte, comp *CompState) []sysexMessage {
	return []sysexMessage{
		{element: element, param: 0x00, channel: channel, value: comp.On},
		{element: element, param: 0x01, channel: channel, value: comp.Threshold},
		{element: element, param: 0x02, channel: channel, value: comp.Ratio},
		{element: element, param: 0x03, channel: channel, value: comp.Attack},
		{element: element, param: 0x04, channel: channel, value: comp.Release},
		{element: element, param: 0x05, channel: channel, value: comp.Knee},
		{element: element, param: 0x06, channel: channel, value: comp.Gain},
	}
}

// RecallMixerState aplica o estado na memória, avisa os clientes web e transmite com cadência segura à mesa
func RecallMixerState(targetState MixerState, paceDelay time.Duration) {
	SetCurrentMixerState(targetState)
	FlushStateToDiskIfDirty()

	// Sincroniza imediatamente todas as abas web conectadas via WebSocket com o snapshot completo
	broadcastWS(WSMessage{
		Type: "state_sync",
		Hex:  "Cena carregada com sucesso",
	})

	// Se tiver saída MIDI física, executa a transmissão em goroutine com cadência suave
	midiLock.Lock()
	out := midiOut
	midiLock.Unlock()

	if out == nil {
		log.Println("Recall de cena: Modo simulação ou sem porta MIDI física. Apenas memória e WebSockets atualizados.")
		return
	}

	msgs := GenerateSysExForState(&targetState)
	log.Printf("Iniciando transmissão de recall para a mesa física: %d comandos SysEx (intervalo: %v)", len(msgs), paceDelay)

	go func(messages []sysexMessage, delay time.Duration) {
		for i, m := range messages {
			sysex := Build01V96SysEx(m.element, m.param, m.channel, m.value)

			midiLock.Lock()
			if midiOut != nil {
				if err := midiOut.Send(sysex); err != nil {
					log.Printf("Aviso ao enviar SysEx [%d/%d]: %v", i+1, len(messages), err)
				}
			}
			midiLock.Unlock()

			if delay > 0 {
				time.Sleep(delay)
			}
		}
		log.Printf("Recall concluído com sucesso: %d comandos SysEx transmitidos à Yamaha 01V96.", len(messages))
	}(msgs, paceDelay)
}

// ==========================================
// HTTP HANDLERS (REST API)
// ==========================================

func requireAdminRole(w http.ResponseWriter, r *http.Request) *Session {
	sess := getSessionFromRequest(r)
	if sess == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return nil
	}
	if !strings.EqualFold(sess.User.Role, "ADMIN") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Acesso restrito para administradores"})
		return nil
	}
	return sess
}

func handleAPIScenes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sess := getSessionFromRequest(r)
	if sess == nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Qualquer usuário autenticado pode listar as cenas
		scenes, err := ListScenes()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"scenes":  scenes,
		})

	case http.MethodPost:
		// Apenas ADMIN pode criar ou salvar cenas
		if requireAdminRole(w, r) == nil {
			return
		}

		var req struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Requisição JSON inválida"})
			return
		}

		scene, err := SaveScene(req.Name, req.Description, sess.User.Name)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"scene":   scene,
			"message": fmt.Sprintf("Cena '%s' salva com sucesso", scene.Name),
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Método não permitido"})
	}
}

func handleAPISceneDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sess := getSessionFromRequest(r)
	if sess == nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return
	}

	// Extrai ID da rota: /api/scenes/{id} ou /api/scenes/{id}/recall
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "ID da cena não especificado"})
		return
	}
	sceneID := parts[2]

	// Sub-rota de recall: POST /api/scenes/{id}/recall
	if len(parts) >= 4 && parts[3] == "recall" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Use POST para recall"})
			return
		}
		if requireAdminRole(w, r) == nil {
			return
		}

		scene, err := GetScene(sceneID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		// Dispara recall cadenciado (6ms entre mensagens = ~3s para toda a mesa com total segurança)
		RecallMixerState(scene.State, 6*time.Millisecond)

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Cena '%s' carregada com sucesso na mesa física!", scene.Name),
			"scene":   scene,
		})
		return
	}

	// Operações sobre a cena individual: GET ou DELETE
	switch r.Method {
	case http.MethodGet:
		// Suporta parâmetro ?download=1 para exportar como anexo
		scene, err := GetScene(sceneID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.json\"", scene.ID))
		}
		json.NewEncoder(w).Encode(scene)

	case http.MethodDelete:
		if requireAdminRole(w, r) == nil {
			return
		}

		if err := DeleteScene(sceneID); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Cena '%s' excluída com sucesso", sceneID),
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Método não permitido"})
	}
}

func handleAPIImportScene(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sess := requireAdminRole(w, r)
	if sess == nil {
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Método não permitido"})
		return
	}

	// Lê arquivo multipart ou JSON bruto
	var rawData []byte
	var err error

	if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(10 << 20); err != nil { // máx 10 MB
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Falha ao processar formulário de upload"})
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Campo de arquivo 'file' obrigatório"})
			return
		}
		defer file.Close()
		rawData, err = io.ReadAll(file)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Falha ao ler arquivo enviado"})
			return
		}
	} else {
		rawData, err = io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Falha ao ler dados da requisição"})
			return
		}
	}

	var imported Scene
	if err := json.Unmarshal(rawData, &imported); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Formato de arquivo JSON de cena inválido"})
		return
	}

	if strings.TrimSpace(imported.Name) == "" {
		imported.Name = "Cena Importada"
	}
	imported.ID = sanitizeSlug(imported.Name)
	imported.Author = sess.User.Name
	imported.UpdatedAt = time.Now()
	if imported.CreatedAt.IsZero() {
		imported.CreatedAt = time.Now()
	}

	data, err := json.MarshalIndent(imported, "", "  ")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Falha ao serializar cena importada"})
		return
	}

	targetPath := filepath.Join(scenesDirRoot, imported.ID+".json")
	if err := os.WriteFile(targetPath, data, 0644); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Falha ao salvar cena importada no disco"})
		return
	}

	log.Printf("Cena '%s' importada com sucesso pelo usuário %s.", imported.Name, sess.User.Username)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Cena '%s' importada com sucesso!", imported.Name),
		"scene":   imported,
	})
}

func handleAPIStateStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sess := getSessionFromRequest(r)
	if sess == nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return
	}

	info, err := os.Stat(lastStateFilePath)
	hasAutoSave := err == nil
	var lastSaveTime time.Time
	var fileSize int64
	if hasAutoSave {
		lastSaveTime = info.ModTime()
		fileSize = info.Size()
	}

	mixerStateLock.RLock()
	isDirty := stateDirty
	mixerStateLock.RUnlock()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":        true,
		"has_auto_save":  hasAutoSave,
		"last_auto_save": lastSaveTime,
		"file_size":      fileSize,
		"is_dirty":       isDirty,
	})
}

func handleAPIRestoreLastState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if requireAdminRole(w, r) == nil {
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Use POST"})
		return
	}

	data, err := os.ReadFile(lastStateFilePath)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Nenhum arquivo de auto-save prévio encontrado"})
		return
	}

	var state MixerState
	if err := json.Unmarshal(data, &state); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Falha ao decodificar auto-save"})
		return
	}

	RecallMixerState(state, 6*time.Millisecond)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Último estado gravado restaurado com sucesso na mesa física!",
	})
}

func handleAPIGetCurrentState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sess := getSessionFromRequest(r)
	if sess == nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return
	}

	state := GetCurrentMixerState()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"state":   state,
	})
}
