package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ==========================================
// ESTRUTURAS DE DADOS DE PRESETS DE CANAL
// ==========================================

// ChannelRackData contém estritamente as informações de rack de canal:
// Equalizador 4 bandas (+ Atenuador digital), Compressor e Noise Gate.
// ATENÇÃO: NÃO guarda volumes (fader principal nem sends), estados de mute,
// configurações de grupo ou estados de pre/post dos envios auxiliares.
type ChannelRackData struct {
	EQ   EQState   `json:"eq"`
	Comp CompState `json:"comp"`
	Gate GateState `json:"gate"`
}

// ChannelPreset encapsula os parâmetros de rack junto com metadados administrativos.
// É salvo exclusivamente em arquivo no serviço (não na mesa física).
type ChannelPreset struct {
	Version     int             `json:"version"`     // 1
	ID          string          `json:"id"`          // Slug único e seguro para arquivo
	Name        string          `json:"name"`        // Nome amigável ("Voz Principal", "Bumbo D6", etc.)
	Description string          `json:"description"` // Comentários adicionais (microfone, instrumento)
	Author      string          `json:"author"`      // Usuário que criou/salvou o preset
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Rack        ChannelRackData `json:"rack"`
}

// ChannelPresetMetadata é um resumo leve para listagem de presets na interface
type ChannelPresetMetadata struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Author      string    `json:"author"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	FileSize    int64     `json:"file_size"`
}

// ==========================================
// GERENCIADOR DE PRESETS EM ARQUIVO NO SERVIÇO
// ==========================================

var (
	channelPresetsLock sync.RWMutex
	channelPresetsDir  = filepath.Join("data", "channel_presets")
)

// InitChannelPresetManager inicializa o diretório e cria presets padrão de fábrica caso a pasta esteja vazia
func InitChannelPresetManager(baseDir string) {
	channelPresetsLock.Lock()
	defer channelPresetsLock.Unlock()

	channelPresetsDir = filepath.Join(baseDir, "channel_presets")
	if err := os.MkdirAll(channelPresetsDir, 0755); err != nil {
		log.Printf("Aviso: Falha ao criar diretório de presets de canal '%s': %v", channelPresetsDir, err)
		return
	}

	// Verifica se a pasta já contém presets salvos
	files, err := os.ReadDir(channelPresetsDir)
	if err != nil {
		log.Printf("Aviso: Erro ao listar presets de canal: %v", err)
		return
	}

	hasJSON := false
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(strings.ToLower(f.Name()), ".json") {
			hasJSON = true
			break
		}
	}

	if !hasJSON {
		seedDefaultChannelPresetsLocked()
	}
}

// seedDefaultChannelPresetsLocked cria presets profissionais de fábrica (Factory Presets)
func seedDefaultChannelPresetsLocked() {
	factoryPresets := []ChannelPreset{
		{
			Version:     1,
			ID:          "vocal-lead-dinamico",
			Name:        "Vocal Lead (Shure SM58 / Beta 58)",
			Description: "HPF 80Hz, corte em 300Hz, presença em 3.5kHz e brilho em 10kHz. Comp suave 3:1.",
			Author:      "Sistema (01V96 Factory)",
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
			Rack: ChannelRackData{
				EQ: EQState{
					On:         1,
					Attenuator: 0,
					HPFOn:      1,
					LPFOn:      0,
					Low:        EQBand{Q: 44, Freq: 16, Gain: 0},   // HPF ~80Hz
					LowMid:     EQBand{Q: 20, Freq: 38, Gain: -25}, // Corte -2.5dB ~300Hz
					HiMid:      EQBand{Q: 20, Freq: 82, Gain: 20},  // Boost +2.0dB ~3.5kHz
					High:       EQBand{Q: 23, Freq: 104, Gain: 15}, // High Shelf +1.5dB ~10kHz
				},
				Comp: CompState{
					On:        1,
					Position:  1,    // Pre Fader
					Threshold: -240, // -24.0 dB
					Ratio:     4,    // 3:1
					Attack:    15,   // 15 ms
					Release:   45,   // ~100 ms
					Knee:      2,    // Soft 2
					Gain:      30,   // +3.0 dB
				},
				Gate: GateState{
					On:        0, // Bypass por segurança em voz
					Threshold: 340,
					Range:     40,
					Attack:    1,
					Hold:      20,
					Decay:     18,
				},
			},
		},
		{
			Version:     1,
			ID:          "bumbo-kick-drum",
			Name:        "Bumbo (Kick Drum)",
			Description: "Punch em 60Hz, corte em 350Hz para limpar embolado, estalo em 3.5kHz. Comp 4:1 com ataque lento e gate rápido.",
			Author:      "Sistema (01V96 Factory)",
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
			Rack: ChannelRackData{
				EQ: EQState{
					On:         1,
					Attenuator: 0,
					HPFOn:      0,
					LPFOn:      0,
					Low:        EQBand{Q: 20, Freq: 12, Gain: 40},  // Boost +4.0dB ~60Hz
					LowMid:     EQBand{Q: 18, Freq: 42, Gain: -60}, // Corte -6.0dB ~350Hz
					HiMid:      EQBand{Q: 22, Freq: 82, Gain: 30},  // Estalo +3.0dB ~3.5kHz
					High:       EQBand{Q: 23, Freq: 100, Gain: 0},
				},
				Comp: CompState{
					On:        1,
					Position:  1,
					Threshold: -180, // -18.0 dB
					Ratio:     6,    // 4:1
					Attack:    30,   // 30 ms deixa passar o transiente
					Release:   35,   // ~80 ms
					Knee:      1,
					Gain:      30, // +3.0 dB
				},
				Gate: GateState{
					On:        1,
					Threshold: 380, // -16.0 dB
					Range:     55,  // atenuação profunda
					Attack:    0,   // 0 ms instantâneo
					Hold:      15,  // 15 ms
					Decay:     10,  // decay curto
				},
			},
		},
		{
			Version:     1,
			ID:          "caixa-snare-drum",
			Name:        "Caixa (Snare Drum)",
			Description: "HPF 100Hz, corpo em 200Hz, corte leve em 600Hz, esteira aberta em 5kHz. Compressor com gate.",
			Author:      "Sistema (01V96 Factory)",
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
			Rack: ChannelRackData{
				EQ: EQState{
					On:         1,
					Attenuator: 0,
					HPFOn:      1,
					LPFOn:      0,
					Low:        EQBand{Q: 44, Freq: 21, Gain: 0},   // HPF 100Hz
					LowMid:     EQBand{Q: 20, Freq: 32, Gain: 25},  // Corpo +2.5dB ~200Hz
					HiMid:      EQBand{Q: 20, Freq: 54, Gain: -20}, // Corte -2.0dB ~600Hz
					High:       EQBand{Q: 23, Freq: 89, Gain: 30},  // Esteira +3.0dB ~5kHz
				},
				Comp: CompState{
					On:        1,
					Position:  1,
					Threshold: -200,
					Ratio:     4,
					Attack:    12,
					Release:   45,
					Knee:      1,
					Gain:      25,
				},
				Gate: GateState{
					On:        1,
					Threshold: 350,
					Range:     45,
					Attack:    1,
					Hold:      20,
					Decay:     14,
				},
			},
		},
		{
			Version:     1,
			ID:          "baixo-eletrico-di",
			Name:        "Baixo Elétrico (Direct Box)",
			Description: "Definição de graves em 80Hz, corte em 400Hz para abrir espaço para bumbo e teclados. Compressão controlada 4:1.",
			Author:      "Sistema (01V96 Factory)",
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
			Rack: ChannelRackData{
				EQ: EQState{
					On:         1,
					Attenuator: 0,
					HPFOn:      0,
					LPFOn:      0,
					Low:        EQBand{Q: 20, Freq: 16, Gain: 25},  // +2.5dB ~80Hz
					LowMid:     EQBand{Q: 20, Freq: 45, Gain: -35}, // -3.5dB ~400Hz
					HiMid:      EQBand{Q: 20, Freq: 72, Gain: 10},  // +1.0dB ~2kHz
					High:       EQBand{Q: 23, Freq: 95, Gain: -10},
				},
				Comp: CompState{
					On:        1,
					Position:  1,
					Threshold: -220,
					Ratio:     6,
					Attack:    20,
					Release:   60,
					Knee:      3,
					Gain:      35,
				},
				Gate: GateState{
					On:        0,
					Threshold: 340,
					Range:     0,
					Attack:    1,
					Hold:      20,
					Decay:     18,
				},
			},
		},
		{
			Version:     1,
			ID:          "violao-aco-acoustico",
			Name:        "Violão Aço (Acoustic Guitar)",
			Description: "HPF 90Hz, corte cirúrgico em 250Hz para remover ressonância de caixa, brilho em 8kHz. Compressão 2.5:1 natural.",
			Author:      "Sistema (01V96 Factory)",
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
			Rack: ChannelRackData{
				EQ: EQState{
					On:         1,
					Attenuator: 0,
					HPFOn:      1,
					LPFOn:      0,
					Low:        EQBand{Q: 44, Freq: 19, Gain: 0},   // HPF ~90Hz
					LowMid:     EQBand{Q: 24, Freq: 35, Gain: -40}, // Corte ressonância -4.0dB ~250Hz
					HiMid:      EQBand{Q: 20, Freq: 78, Gain: 10},  // Presença +1.0dB ~3kHz
					High:       EQBand{Q: 23, Freq: 98, Gain: 25},  // Brilho +2.5dB ~8kHz
				},
				Comp: CompState{
					On:        1,
					Position:  1,
					Threshold: -220,
					Ratio:     3,
					Attack:    25,
					Release:   50,
					Knee:      3,
					Gain:      20,
				},
				Gate: GateState{
					On:        0,
					Threshold: 340,
					Range:     0,
					Attack:    1,
					Hold:      20,
					Decay:     18,
				},
			},
		},
		{
			Version:     1,
			ID:          "fala-locucao-pulpito",
			Name:        "Fala / Locução / Púlpito",
			Description: "HPF alto em 120Hz para cortar baques de pedestais e manuseio, clareza em 2.5kHz. Compressão suave.",
			Author:      "Sistema (01V96 Factory)",
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
			Rack: ChannelRackData{
				EQ: EQState{
					On:         1,
					Attenuator: 0,
					HPFOn:      1,
					LPFOn:      0,
					Low:        EQBand{Q: 44, Freq: 24, Gain: 0},   // HPF ~120Hz
					LowMid:     EQBand{Q: 20, Freq: 36, Gain: -30}, // -3.0dB ~250Hz
					HiMid:      EQBand{Q: 20, Freq: 75, Gain: 20},  // +2.0dB ~2.5kHz
					High:       EQBand{Q: 23, Freq: 102, Gain: 10}, // +1.0dB ~10kHz
				},
				Comp: CompState{
					On:        1,
					Position:  1,
					Threshold: -250,
					Ratio:     3,
					Attack:    10,
					Release:   55,
					Knee:      4,
					Gain:      30,
				},
				Gate: GateState{
					On:        1,
					Threshold: 220, // -32 dB
					Range:     35,  // atenuação leve sem cortar respiração
					Attack:    2,
					Hold:      30,
					Decay:     20,
				},
			},
		},
	}

	for _, p := range factoryPresets {
		filePath := filepath.Join(channelPresetsDir, p.ID+".json")
		data, err := json.MarshalIndent(p, "", "  ")
		if err == nil {
			_ = os.WriteFile(filePath, data, 0644)
		}
	}
	log.Printf("Presets de canal padrão de fábrica instalados em '%s'.", channelPresetsDir)
}

// sanitizePresetSlug gera um identificador seguro para nome de arquivo
func sanitizePresetSlug(name string) string {
	slug := sanitizeSlug(name)
	if slug == "" {
		slug = fmt.Sprintf("preset-%d", time.Now().Unix())
	}
	return slug
}

// SaveChannelPreset salva ou atualiza um preset de canal exclusivamente em arquivo no serviço
func SaveChannelPreset(name, description, author string, rack ChannelRackData) (*ChannelPreset, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("o nome do preset de canal não pode ser vazio")
	}

	channelPresetsLock.Lock()
	defer channelPresetsLock.Unlock()

	if err := os.MkdirAll(channelPresetsDir, 0755); err != nil {
		return nil, fmt.Errorf("falha ao criar pasta de presets: %w", err)
	}

	baseSlug := sanitizePresetSlug(name)
	id := baseSlug

	filePath := filepath.Join(channelPresetsDir, id+".json")
	if _, err := os.Stat(filePath); err == nil {
		existing, readErr := readChannelPresetFileLocked(filePath)
		if readErr == nil && !strings.EqualFold(existing.Name, name) {
			id = fmt.Sprintf("%s-%d", baseSlug, time.Now().Unix()%10000)
			filePath = filepath.Join(channelPresetsDir, id+".json")
		}
	}

	now := time.Now().UTC()
	preset := &ChannelPreset{
		Version:     1,
		ID:          id,
		Name:        name,
		Description: strings.TrimSpace(description),
		Author:      author,
		CreatedAt:   now,
		UpdatedAt:   now,
		Rack:        rack,
	}

	if existing, err := readChannelPresetFileLocked(filePath); err == nil && existing != nil {
		preset.CreatedAt = existing.CreatedAt
	}

	data, err := json.MarshalIndent(preset, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("erro ao serializar preset JSON: %w", err)
	}

	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return nil, fmt.Errorf("falha ao gravar arquivo temporário de preset: %w", err)
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		_ = os.Remove(tmpFile)
		return nil, fmt.Errorf("falha ao salvar preset: %w", err)
	}

	log.Printf("Preset de canal salvo em arquivo no serviço: '%s' (ID: %s, por %s)", preset.Name, preset.ID, preset.Author)
	return preset, nil
}

// readChannelPresetFileLocked lê e decodifica um arquivo de preset
func readChannelPresetFileLocked(path string) (*ChannelPreset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var preset ChannelPreset
	if err := json.Unmarshal(data, &preset); err != nil {
		return nil, err
	}

	return &preset, nil
}

// GetChannelPreset recupera um preset específico pelo ID a partir do arquivo no serviço
func GetChannelPreset(id string) (*ChannelPreset, error) {
	id = strings.TrimSpace(id)
	cleanID := filepath.Base(id)
	if cleanID == "." || cleanID == "/" || cleanID == "" || strings.Contains(id, "..") {
		return nil, fmt.Errorf("ID de preset inválido")
	}

	channelPresetsLock.RLock()
	defer channelPresetsLock.RUnlock()

	filePath := filepath.Join(channelPresetsDir, cleanID+".json")
	preset, err := readChannelPresetFileLocked(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("preset de canal '%s' não encontrado", cleanID)
		}
		return nil, fmt.Errorf("erro ao ler arquivo de preset: %w", err)
	}

	return preset, nil
}

// ListChannelPresets retorna a lista resumida de todos os presets disponíveis no serviço
func ListChannelPresets() ([]ChannelPresetMetadata, error) {
	channelPresetsLock.RLock()
	defer channelPresetsLock.RUnlock()

	if err := os.MkdirAll(channelPresetsDir, 0755); err != nil {
		return nil, fmt.Errorf("falha ao acessar pasta de presets: %w", err)
	}

	entries, err := os.ReadDir(channelPresetsDir)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler diretório de presets: %w", err)
	}

	var list []ChannelPresetMetadata
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}

		filePath := filepath.Join(channelPresetsDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		preset, err := readChannelPresetFileLocked(filePath)
		if err != nil {
			continue
		}

		list = append(list, ChannelPresetMetadata{
			ID:          preset.ID,
			Name:        preset.Name,
			Description: preset.Description,
			Author:      preset.Author,
			CreatedAt:   preset.CreatedAt,
			UpdatedAt:   preset.UpdatedAt,
			FileSize:    info.Size(),
		})
	}

	sort.Slice(list, func(i, j int) bool {
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})

	return list, nil
}

// DeleteChannelPreset remove permanentemente o arquivo de preset salvo no serviço
func DeleteChannelPreset(id string) error {
	id = strings.TrimSpace(id)
	cleanID := filepath.Base(id)
	if cleanID == "." || cleanID == "/" || cleanID == "" || strings.Contains(id, "..") {
		return fmt.Errorf("ID de preset inválido")
	}

	channelPresetsLock.Lock()
	defer channelPresetsLock.Unlock()

	filePath := filepath.Join(channelPresetsDir, cleanID+".json")
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("preset '%s' não existe", cleanID)
	}

	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("falha ao excluir preset: %w", err)
	}

	log.Printf("Arquivo de preset de canal '%s' removido com sucesso do serviço.", cleanID)
	return nil
}

// GenerateRackSysExForChannel gera a lista atômica de comandos SysEx para replicar o rack completo no Edit Buffer do canal
func GenerateRackSysExForChannel(channel byte, rack *ChannelRackData) []sysexMessage {
	var msgs []sysexMessage

	// 1. Attenuator Digital (0x1D param 0x00)
	msgs = append(msgs, sysexMessage{element: 0x1D, param: 0x00, channel: channel, value: rack.EQ.Attenuator})

	// 2. Equalizador Paramétrico de 4 Bandas (0x20)
	msgs = append(msgs, generateEQSysEx(0x20, channel, &rack.EQ)...)

	// 3. Compressor de Canal (0x1F)
	msgs = append(msgs, generateCompSysEx(0x1F, channel, &rack.Comp)...)

	// 4. Noise Gate de Canal (0x21)
	msgs = append(msgs, generateGateSysEx(0x21, channel, &rack.Gate)...)

	return msgs
}

// ApplyChannelPresetToChannel carrega o preset do arquivo no serviço e o aplica ao Edit Buffer do canal especificado (1 a 32).
// Atualiza a memória de software do serviço, transmite com cadência segura à mesa física e notifica WebSockets.
func ApplyChannelPresetToChannel(presetID string, channelNum int, author string, paceDelay time.Duration) (*ChannelPreset, error) {
	if channelNum < 1 || channelNum > 32 {
		return nil, fmt.Errorf("canal de entrada inválido: %d (deve ser entre 1 e 32)", channelNum)
	}

	preset, err := GetChannelPreset(presetID)
	if err != nil {
		return nil, err
	}

	chIdx := channelNum - 1
	chByte := byte(chIdx)

	// 1. Aplica na memória de software do serviço (currentMixer)
	mixerStateLock.Lock()
	currentMixer.Inputs[chIdx].EQ = preset.Rack.EQ
	currentMixer.Inputs[chIdx].Comp = preset.Rack.Comp
	currentMixer.Inputs[chIdx].Gate = preset.Rack.Gate
	stateDirty = true
	mixerStateLock.Unlock()

	FlushStateToDiskIfDirty()

	// 2. Gera mensagens SysEx atômicas para o Edit Buffer do canal na mesa
	msgs := GenerateRackSysExForChannel(chByte, &preset.Rack)

	// 3. Notifica clientes WebSocket
	for _, m := range msgs {
		broadcastWS(WSMessage{
			Type:    "param",
			Element: m.element,
			Param:   m.param,
			Channel: m.channel,
			Value:   m.value,
		})
	}

	broadcastWS(WSMessage{
		Type:    "channel_preset_applied",
		Channel: chByte,
		Hex:     fmt.Sprintf("Preset '%s' aplicado ao CH %02d por %s", preset.Name, channelNum, author),
	})

	// 4. Transmissão para a porta MIDI física (Edit Buffer da 01V96) com cadência suave
	midiLock.Lock()
	out := midiOut
	midiLock.Unlock()

	if out != nil {
		go func(messages []sysexMessage, delay time.Duration) {
			for i, m := range messages {
				sysex := Build01V96SysEx(m.element, m.param, m.channel, m.value)
				markSentEcho(m.element, m.param, m.channel, m.value)

				midiLock.Lock()
				if err := sendMIDISysExLocked(sysex); err != nil {
					log.Printf("Aviso ao enviar SysEx Preset [%d/%d]: %v", i+1, len(messages), err)
				}
				midiLock.Unlock()

				if delay > 0 {
					time.Sleep(delay)
				}
			}
			log.Printf("Preset '%s' carregado com sucesso no Edit Buffer do Canal %02d (%d SysEx transmitidos à mesa).",
				preset.Name, channelNum, len(messages))
		}(msgs, paceDelay)
	} else {
		log.Printf("Preset '%s' aplicado ao Canal %02d em software/WebSockets (modo simulação).", preset.Name, channelNum)
	}

	return preset, nil
}

// ==========================================
// CONTROLE DE ACESSO E RBAC (OPERATOR+)
// ==========================================

// requireOperatorRole valida se a requisição é de um usuário autenticado com papel OPERATOR ou ADMIN.
// Usuários BASIC ou PERFORMER recebem 403 Forbidden.
func requireOperatorRole(w http.ResponseWriter, r *http.Request) *Session {
	sess := getSessionFromRequest(r)
	if sess == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return nil
	}

	role := strings.ToUpper(strings.TrimSpace(sess.User.Role))
	if role != "ADMIN" && role != "OPERATOR" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Acesso restrito para operadores e administradores (operator+)",
		})
		return nil
	}

	return sess
}

// ==========================================
// HANDLERS HTTP (API REST)
// ==========================================

// handleAPIChannelPresets gerencia a listagem e criação de presets de canal em arquivos no serviço
// GET /api/channel-presets (Autenticado)
// POST /api/channel-presets (Operator+)
func handleAPIChannelPresets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sess := getSessionFromRequest(r)
	if sess == nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		presets, err := ListChannelPresets()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"presets": presets,
		})

	case http.MethodPost:
		// Apenas OPERATOR ou ADMIN podem salvar presets
		if requireOperatorRole(w, r) == nil {
			return
		}

		var req struct {
			Name        string           `json:"name"`
			Description string           `json:"description"`
			Channel     int              `json:"channel"` // 1 a 32 (opcional se rack for informado)
			Rack        *ChannelRackData `json:"rack"`    // opcional se channel for informado
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "JSON inválido"})
			return
		}

		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Nome do preset é obrigatório"})
			return
		}

		var rackToSave ChannelRackData
		if req.Rack != nil {
			rackToSave = *req.Rack
		} else if req.Channel >= 1 && req.Channel <= 32 {
			chIdx := req.Channel - 1
			mixerStateLock.RLock()
			rackToSave = ChannelRackData{
				EQ:   currentMixer.Inputs[chIdx].EQ,
				Comp: currentMixer.Inputs[chIdx].Comp,
				Gate: currentMixer.Inputs[chIdx].Gate,
			}
			mixerStateLock.RUnlock()
		} else {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Especifique o canal (1 a 32) de onde capturar o rack ou forneça os dados do rack no corpo da requisição",
			})
			return
		}

		preset, err := SaveChannelPreset(req.Name, req.Description, sess.User.Name, rackToSave)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"preset":  preset,
			"message": fmt.Sprintf("Preset de canal '%s' salvo em arquivo com sucesso!", preset.Name),
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Método não permitido"})
	}
}

// handleAPIChannelPresetDetail gerencia obtenção, aplicação e exclusão de um preset
// GET /api/channel-presets/{id}
// POST /api/channel-presets/{id}/apply (Operator+)
// DELETE /api/channel-presets/{id} (Operator+)
func handleAPIChannelPresetDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sess := getSessionFromRequest(r)
	if sess == nil {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Não autenticado"})
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "ID do preset não especificado"})
		return
	}

	presetID := parts[2]

	// Sub-rota: /api/channel-presets/{id}/apply
	if len(parts) >= 4 && parts[3] == "apply" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Use POST para aplicar preset"})
			return
		}

		// Apenas OPERATOR ou ADMIN podem carregar presets
		if requireOperatorRole(w, r) == nil {
			return
		}

		var req struct {
			Channel int `json:"channel"` // 1 a 32
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "JSON inválido"})
			return
		}

		if req.Channel < 1 || req.Channel > 32 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Canal %d inválido. Deve ser entre 1 e 32.", req.Channel),
			})
			return
		}

		appliedPreset, err := ApplyChannelPresetToChannel(presetID, req.Channel, sess.User.Name, 6*time.Millisecond)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"preset":  appliedPreset,
			"message": fmt.Sprintf("Preset '%s' carregado no Canal %02d com sucesso!", appliedPreset.Name, req.Channel),
		})
		return
	}

	switch r.Method {
	case http.MethodGet:
		preset, err := GetChannelPreset(presetID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"channel-preset-%s.json\"", preset.ID))
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"preset":  preset,
		})

	case http.MethodDelete:
		// Apenas OPERATOR ou ADMIN podem excluir presets
		if requireOperatorRole(w, r) == nil {
			return
		}

		if err := DeleteChannelPreset(presetID); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Preset '%s' excluído do serviço com sucesso", presetID),
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Método não permitido"})
	}
}

// handleAPIImportChannelPreset permite upload de arquivo de preset JSON para o serviço
func handleAPIImportChannelPreset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Use POST para importar preset"})
		return
	}

	sess := requireOperatorRole(w, r)
	if sess == nil {
		return
	}

	var data []byte
	contentType := r.Header.Get("Content-Type")

	if strings.Contains(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(5 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Falha ao processar formulário multipart"})
			return
		}

		file, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Campo de arquivo 'file' obrigatório"})
			return
		}
		defer file.Close()

		data, err = io.ReadAll(file)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Falha ao ler arquivo de upload"})
			return
		}
	} else {
		var err error
		data, err = io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Falha ao ler corpo da requisição"})
			return
		}
	}

	var preset ChannelPreset
	if err := json.Unmarshal(data, &preset); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Formato de arquivo JSON de preset inválido"})
		return
	}

	if strings.TrimSpace(preset.Name) == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "O arquivo de preset não possui um nome válido"})
		return
	}

	saved, err := SaveChannelPreset(preset.Name, preset.Description, sess.User.Name, preset.Rack)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"preset":  saved,
		"message": fmt.Sprintf("Preset '%s' importado com sucesso!", saved.Name),
	})
}

