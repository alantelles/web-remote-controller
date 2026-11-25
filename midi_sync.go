package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/drivers"
)

// ==========================================
// DECODIFICAÇÃO E CODIFICAÇÃO SYSEX (28 BITS)
// ==========================================

// Decode28BitValue converte 4 bytes de 7 bits (dd1..dd4) em complemento de 2 para inteiro com sinal.
// A Yamaha divide um inteiro com sinal de 28 bits através de 4 bytes de dados SysEx:
// dd1 (bits 27..21, com bit 27 como sinal), dd2 (bits 20..14), dd3 (bits 13..7) e dd4 (bits 6..0).
func Decode28BitValue(dd1, dd2, dd3, dd4 byte) int {
	raw := (uint32(dd1&0x7F) << 21) |
		(uint32(dd2&0x7F) << 14) |
		(uint32(dd3&0x7F) << 7) |
		uint32(dd4&0x7F)

	if (raw & 0x08000000) != 0 {
		// Número negativo: estende o bit 27 para os 32 bits inteiros
		return int(int32(raw | 0xF0000000))
	}
	return int(raw)
}

// Encode28BitValue codifica um valor inteiro com sinal em 4 bytes de 7 bits no padrão Yamaha.
func Encode28BitValue(value int) (dd1, dd2, dd3, dd4 byte) {
	u := uint32(int32(value)) & 0x0FFFFFFF
	dd1 = byte((u >> 21) & 0x7F)
	dd2 = byte((u >> 14) & 0x7F)
	dd3 = byte((u >> 7) & 0x7F)
	dd4 = byte(u & 0x7F)
	return
}

// Parse01V96SysEx decodifica uma mensagem SysEx de 14 bytes da Yamaha 01V96 / Studio Manager.
// Estrutura esperada:
// F0 43 1n 3E [0D|7F] 01 ee pp cc dd1 dd2 dd3 dd4 F7
func Parse01V96SysEx(data []byte) (element, param, channel byte, value int, ok bool) {
	if len(data) != 14 {
		return 0, 0, 0, 0, false
	}
	if data[0] != 0xF0 || data[1] != 0x43 || (data[2]&0xF0) != 0x10 || data[3] != 0x3E {
		return 0, 0, 0, 0, false
	}
	// Model ID: 0x7F (Universal Digital Mixer) ou 0x0D (01V96 específico)
	if data[4] != 0x7F && data[4] != 0x0D {
		return 0, 0, 0, 0, false
	}
	// Data Type: 0x01 (Edit Buffer) e EOX: 0xF7
	if data[5] != 0x01 || data[13] != 0xF7 {
		return 0, 0, 0, 0, false
	}

	element = data[6]
	param = data[7]
	channel = data[8]
	value = Decode28BitValue(data[9], data[10], data[11], data[12])
	return element, param, channel, value, true
}

// Build01V96ParamRequest monta a mensagem SysEx de solicitação de parâmetro (Parameter Request, 10 bytes).
// Formato Universal Yamaha Digital Mixer: F0 43 30 3E 7F 01 ee pp cc F7
func Build01V96ParamRequest(element, param, channel byte) []byte {
	return []byte{
		0xF0, 0x43, 0x30, 0x3E, 0x7F, 0x01,
		element, param, channel,
		0xF7,
	}
}

// Parse01V96ChannelNameSysEx decodifica um caractere do nome de canal (curto: 0..3; longo: 0x08..0x17) da 01V96 / Studio Manager.
// Formato: F0 43 1n 3E [0D|7F] 02 04 [param] [ch 0..31] 00 00 00 [ascii] F7
func Parse01V96ChannelNameSysEx(data []byte) (channel byte, param byte, ascii byte, ok bool) {
	if len(data) != 14 {
		return 0, 0, 0, false
	}
	if data[0] != 0xF0 || data[1] != 0x43 || (data[2]&0xF0) != 0x10 || data[3] != 0x3E {
		return 0, 0, 0, false
	}
	if data[4] != 0x0D && data[4] != 0x7F {
		return 0, 0, 0, false
	}
	if data[5] != 0x02 || data[6] != 0x04 || data[13] != 0xF7 {
		return 0, 0, 0, false
	}
	param = data[7]
	channel = data[8]
	isShort := param < 4
	isLong := param >= 0x08 && param <= 0x17
	if (!isShort && !isLong) || channel >= 32 {
		return 0, 0, 0, false
	}
	ascii = data[12] & 0x7F
	return channel, param, ascii, true
}

// Build01V96MeterRequest monta a mensagem SysEx de solicitação de Remote Meter (12 bytes).
// Formato Yamaha 01V96: F0 43 30 3E 0D 21 [UL] [LU] [LL] [CountH] [CountL] F7
func Build01V96MeterRequest(ul, lu, ll byte, count uint16) []byte {
	return []byte{
		0xF0, 0x43, 0x30, 0x3E, 0x0D, 0x21,
		ul & 0x7F, lu & 0x7F, ll & 0x7F,
		byte((count >> 7) & 0x7F), byte(count & 0x7F),
		0xF7,
	}
}

// Build01V96MeterStop monta a mensagem SysEx para interromper o stream de Remote Meter (UL = 0x7F).
func Build01V96MeterStop() []byte {
	return []byte{0xF0, 0x43, 0x30, 0x3E, 0x0D, 0x21, 0x7F, 0x00, 0x00, 0x00, 0x00, 0xF7}
}

// Parse01V96MeterSysEx decodifica pacotes de resposta Remote Meter (0x21) da Yamaha 01V96.
// Formato esperado: F0 43 1n 3E [0D|7F] 21 [UL] [LU] [LL] [H1 L1] [H2 L2] ... F7
func Parse01V96MeterSysEx(data []byte) (ul, lu, ll byte, values []int, ok bool) {
	if len(data) < 12 || (len(data)-10)%2 != 0 {
		return 0, 0, 0, nil, false
	}
	if data[0] != 0xF0 || data[1] != 0x43 || (data[2]&0xF0) != 0x10 || data[3] != 0x3E {
		return 0, 0, 0, nil, false
	}
	if data[4] != 0x0D && data[4] != 0x7F {
		return 0, 0, 0, nil, false
	}
	if data[5] != 0x21 || data[len(data)-1] != 0xF7 {
		return 0, 0, 0, nil, false
	}

	ul = data[6]
	lu = data[7]
	ll = data[8]
	payload := data[9 : len(data)-1]
	values = make([]int, len(payload)/2)
	for i := 0; i < len(values); i++ {
		h := int(payload[i*2] & 0x7F)
		l := int(payload[i*2+1] & 0x7F)
		values[i] = (h << 7) | l
	}
	return ul, lu, ll, values, true
}

// ==========================================
// SUPRESSÃO DE ECO LOCAL (LOOPBACK FILTER)
// ==========================================

type echoKey struct {
	element byte
	param   byte
	channel byte
	value   int
}

type echoFilter struct {
	mu      sync.Mutex
	entries map[echoKey]time.Time
	window  time.Duration
}

var globalEchoFilter = &echoFilter{
	entries: make(map[echoKey]time.Time),
	window:  350 * time.Millisecond,
}

// markSentEcho registra que um comando SysEx foi transmitido pela aplicação,
// para que caso ele retorne via porta de loopback (ex: Midi Through / VirMIDI),
// seja descartado e não cause jitter ou duplicações.
func markSentEcho(element, param, channel byte, value int) {
	globalEchoFilter.mu.Lock()
	defer globalEchoFilter.mu.Unlock()

	now := time.Now()
	// Limpeza periódica de entradas expiradas se o mapa crescer
	if len(globalEchoFilter.entries) > 200 {
		for k, exp := range globalEchoFilter.entries {
			if now.After(exp) {
				delete(globalEchoFilter.entries, k)
			}
		}
	}

	k := echoKey{element, param, channel, value}
	globalEchoFilter.entries[k] = now.Add(globalEchoFilter.window)
}

// isSentEcho verifica e consome o registro se a mensagem foi originada pela própria aplicação.
func isSentEcho(element, param, channel byte, value int) bool {
	globalEchoFilter.mu.Lock()
	defer globalEchoFilter.mu.Unlock()

	k := echoKey{element, param, channel, value}
	exp, exists := globalEchoFilter.entries[k]
	if !exists {
		return false
	}

	delete(globalEchoFilter.entries, k)
	if time.Now().Before(exp) {
		return true
	}
	return false
}

// ==========================================
// DESCRIÇÃO HUMANA DE PARÂMETROS PARA LOGS
// ==========================================

func describe01V96Param(element, param, channel byte, value int) string {
	chNum := int(channel) + 1

	eqParamNames := map[byte]string{
		0x0F: "EQ ON/OFF",
		0x01: "LOW Q/Type",
		0x02: "LOW Freq",
		0x03: "LOW Gain",
		0x04: "HPF ON/OFF",
		0x05: "LO-MID Q",
		0x06: "LO-MID Freq",
		0x07: "LO-MID Gain",
		0x08: "HI-MID Q",
		0x09: "HI-MID Freq",
		0x0A: "HI-MID Gain",
		0x0B: "HIGH Q/Type",
		0x0C: "HIGH Freq",
		0x0D: "HIGH Gain",
		0x0E: "LPF ON/OFF",
	}

	compParamNames := map[byte]string{
		0x00: "Position (Insert)",
		0x01: "COMP ON/OFF",
		0x04: "Attack",
		0x05: "Release",
		0x06: "Ratio",
		0x07: "Make-up Gain",
		0x08: "Knee",
		0x09: "Threshold",
	}

	gateParamNames := map[byte]string{
		0x00: "GATE ON/OFF",
		0x01: "Threshold",
		0x02: "Range",
		0x03: "Attack",
		0x04: "Hold",
		0x05: "Decay",
	}

	switch element {
	case 0x1C:
		return fmt.Sprintf("CH %02d -> MASTER Fader: %d (%s)", chNum, value, faderValueToDB(value))
	case 0x1A:
		onStr := "MUTE (0)"
		if value == 1 {
			onStr = "ON (1)"
		}
		return fmt.Sprintf("CH %02d -> MASTER Channel: %s", chNum, onStr)
	case 0x1B:
		panStr := "C (0)"
		if value > 0 {
			panStr = fmt.Sprintf("R%d", value)
		} else if value < 0 {
			panStr = fmt.Sprintf("L%d", -value)
		}
		return fmt.Sprintf("CH %02d -> PAN: %s", chNum, panStr)
	case 0x1D:
		return fmt.Sprintf("CH %02d -> ATTENUATOR: %+.1f dB (%d)", chNum, float64(value)/10.0, value)
	case 0x23:
		auxNum := int(param/3) + 1
		subParam := param % 3
		switch subParam {
		case 0:
			onStr := "MUTE (0)"
			if value == 1 {
				onStr = "ON (1)"
			}
			return fmt.Sprintf("CH %02d -> AUX %d Send: %s", chNum, auxNum, onStr)
		case 1:
			preStr := "POST-Fader (0)"
			if value == 1 {
				preStr = "PRE-Fader (1)"
			}
			return fmt.Sprintf("CH %02d -> AUX %d Routing: %s", chNum, auxNum, preStr)
		case 2:
			return fmt.Sprintf("CH %02d -> AUX %d Send Level: %d (%s)", chNum, auxNum, value, faderValueToDB(value))
		default:
			return fmt.Sprintf("CH %02d -> AUX %d Param 0x%02X: %d", chNum, auxNum, param, value)
		}
	case 0x20:
		pName := eqParamNames[param]
		if pName == "" {
			pName = fmt.Sprintf("Param 0x%02X", param)
		}
		valDesc := fmt.Sprintf("%d", value)
		if param == 0x03 || param == 0x07 || param == 0x0A || param == 0x0D {
			valDesc = fmt.Sprintf("%.1f dB (%d)", float64(value)/10.0, value)
		}
		return fmt.Sprintf("CH %02d -> EQ %s: %s", chNum, pName, valDesc)
	case 0x1F:
		pName := compParamNames[param]
		if pName == "" {
			pName = fmt.Sprintf("Param 0x%02X", param)
		}
		valDesc := fmt.Sprintf("%d", value)
		if param == 0x09 {
			valDesc = fmt.Sprintf("%.1f dB (%d)", float64(value)/10.0, value)
		} else if param == 0x07 {
			valDesc = fmt.Sprintf("+%.1f dB (%d)", float64(value)/10.0, value)
		}
		return fmt.Sprintf("CH %02d -> COMP %s: %s", chNum, pName, valDesc)
	case 0x21:
		pName := gateParamNames[param]
		if pName == "" {
			pName = fmt.Sprintf("Param 0x%02X", param)
		}
		return fmt.Sprintf("CH %02d -> GATE %s: %d", chNum, pName, value)
	case 0x4F:
		return fmt.Sprintf("STEREO MASTER Out Fader: %d (%s)", value, faderOutputValueToDB(value))
	case 0x4D:
		onStr := "MUTE (0)"
		if value == 1 {
			onStr = "ON (1)"
		}
		return fmt.Sprintf("STEREO MASTER Out: %s", onStr)
	case 0x39:
		auxNum := channel + 1
		return fmt.Sprintf("AUX %d MASTER Fader: %d (%s)", auxNum, value, faderOutputValueToDB(value))
	case 0x36:
		auxNum := channel + 1
		onStr := "MUTE (0)"
		if value == 1 {
			onStr = "ON (1)"
		}
		return fmt.Sprintf("AUX %d MASTER Out: %s", auxNum, onStr)
	case 0x35:
		auxNum := channel + 1
		pName := eqParamNames[param]
		if pName == "" {
			pName = fmt.Sprintf("Param 0x%02X", param)
		}
		return fmt.Sprintf("AUX %d MASTER Out -> EQ %s: %d", auxNum, pName, value)
	case 0x50:
		pName := eqParamNames[param]
		if pName == "" {
			pName = fmt.Sprintf("Param 0x%02X", param)
		}
		return fmt.Sprintf("STEREO MASTER Out -> EQ %s: %d", pName, value)
	case 0x51:
		pName := compParamNames[param]
		if pName == "" {
			pName = fmt.Sprintf("Param 0x%02X", param)
		}
		return fmt.Sprintf("STEREO MASTER Out -> COMP %s: %d", pName, value)
	default:
		return fmt.Sprintf("CH %02d -> Element 0x%02X, Param 0x%02X: %d", chNum, element, param, value)
	}
}

// faderOutputValueToDB converte valor de fader de saída (0..1023) na escala dBFS da 01V96 (-∞ a 0 dB)
func faderOutputValueToDB(v int) string {
	if v <= 0 {
		return "-∞ dB"
	}
	if v >= 1023 {
		return "0.0 dB"
	}
	type calPoint struct {
		val int
		db  float64
	}
	cal := []calPoint{
		{0, -99.9},
		{100, -50.0},
		{200, -40.0},
		{340, -30.0},
		{480, -20.0},
		{620, -15.0},
		{760, -10.0},
		{890, -5.0},
		{1023, 0.0},
	}
	for i := 0; i < len(cal)-1; i++ {
		if v >= cal[i].val && v <= cal[i+1].val {
			ratio := float64(v-cal[i].val) / float64(cal[i+1].val-cal[i].val)
			db := cal[i].db + ratio*(cal[i+1].db-cal[i].db)
			return fmt.Sprintf("%.1f dB", db)
		}
	}
	return "0.0 dB"
}

// ==========================================
// ESCUTA MIDI IN EM TEMPO REAL
// ==========================================

var (
	midiIn         drivers.In
	midiInStopFunc func()
	midiInLock     sync.Mutex
)

// handleIncomingMIDIMessage processa cada mensagem que chega pela porta MIDI IN
func handleIncomingMIDIMessage(msg midi.Message, timestampms int32) {
	data := msg.Bytes()

	// 1. Mensagem SysEx da 01V96 (Parameter Change em tempo real)
	if element, param, channel, value, ok := Parse01V96SysEx(data); ok {
		// Descarta eco emitido por nós mesmos em conexões loopback (ex: Midi Through ou VirMIDI)
		if isSentEcho(element, param, channel, value) {
			return
		}

		syncReplyCounter.Add(1)

		// Atualiza o espelho de estado em memória (scenes/MixerState)
		ApplyParamToState(element, param, channel, value)

		hexStr := fmt.Sprintf("% X", data)
		desc := describe01V96Param(element, param, channel, value)
		log.Printf("MIDI IN -> SysEx: %s | %s", hexStr, desc)

		// Transmite via WebSocket a todas as abas e dispositivos conectados
		broadcastWS(WSMessage{
			Type:    "param",
			Element: element,
			Param:   param,
			Channel: channel,
			Value:   value,
			Hex:     hexStr,
		})
		return
	}

	// 2. Mensagem SysEx de Remote Meter (0x21 - Level / Gain Reduction)
	if ul, lu, ll, values, ok := Parse01V96MeterSysEx(data); ok {
		handleIncomingMeterData(ul, lu, ll, values, data)
		return
	}

	// 3. Log de diagnóstico para outros pacotes SysEx recebidos da mesa
	if len(data) > 0 && data[0] == 0xF0 {
		log.Printf("MIDI IN -> SysEx não mapeado (%d bytes): % X", len(data), data)
		return
	}

	// 4. Program Change recebido da mesa (indica recall físico de cena na 01V96)
	var ch, prog uint8
	if msg.GetProgramChange(&ch, &prog) {
		log.Printf("MIDI IN -> Program Change recebido (Ch: %d, Prog: %d). Cena alterada na mesa física. Disparando sincronização...", ch, prog)
		go RequestMixerInitialState(15 * time.Millisecond)
		return
	}
}

// startMIDIInListener inicia a escuta assíncrona na porta de entrada MIDI configurada
func startMIDIInListener(in drivers.In) (stopFn func(), err error) {
	midiInLock.Lock()
	defer midiInLock.Unlock()

	if in == nil {
		return func() {}, nil
	}

	stop, err := midi.ListenTo(in, handleIncomingMIDIMessage, midi.UseSysEx())
	if err != nil {
		return nil, fmt.Errorf("falha ao ativar escuta na porta MIDI IN '%s': %w", in.String(), err)
	}

	midiIn = in
	midiInStopFunc = stop
	return stop, nil
}

// ==========================================
// SINCRONIZAÇÃO DE ESTADO INICIAL DA MESA
// ==========================================

type paramTarget struct {
	dataType byte // 0 ou 0x01 = Parameter Change; 0x02 = Channel Name
	element  byte
	param    byte
	channel  byte
}

var (
	mixerSyncLock      sync.Mutex
	mixerSyncActive    atomic.Bool
	mixerSyncProgress  atomic.Int32
	mixerSyncTotalReqs atomic.Int32
	syncReplyCounter   atomic.Int32
)

// IsMixerSyncing retorna se uma sincronização com a mesa física está em andamento
func IsMixerSyncing() bool {
	return mixerSyncActive.Load()
}

// generateSyncRequestList cria a lista completa e prioritária de solicitações para ler a mesa física
func generateSyncRequestList() (critical []paramTarget, detail []paramTarget) {
	// 1. CRÍTICOS (Faders principais, Chaves ON/MUTE e Panorâmica)
	// Stereo Master
	critical = append(critical, paramTarget{element: 0x4F, param: 0x00, channel: 0x00}) // Stereo Fader
	critical = append(critical, paramTarget{element: 0x4D, param: 0x00, channel: 0x00}) // Stereo ON

	// Aux Masters 1 a 8
	for i := 0; i < 8; i++ {
		auxCh := byte(i)
		critical = append(critical, paramTarget{element: 0x39, param: 0x00, channel: auxCh}) // Aux Master Fader
		critical = append(critical, paramTarget{element: 0x36, param: 0x00, channel: auxCh}) // Aux Master ON
	}

	// Canais de Entrada 1 a 32 (Fader, Mute, Pan)
	for i := 0; i < 32; i++ {
		ch := byte(i)
		critical = append(critical, paramTarget{element: 0x1C, param: 0x00, channel: ch}) // Fader
		critical = append(critical, paramTarget{element: 0x1A, param: 0x00, channel: ch}) // ON / MUTE
		critical = append(critical, paramTarget{element: 0x1B, param: 0x00, channel: ch}) // PAN
	}

	// 2. DETALHADOS (Aux Sends, Attenuators, Equalizadores e Compressores)
	// Aux Sends dos Canais 1 a 32 para Auxiliares 1 a 8 (ON, PRE/POST, LEVEL)
	for i := 0; i < 32; i++ {
		ch := byte(i)
		detail = append(detail, paramTarget{element: 0x1D, param: 0x00, channel: ch}) // Attenuator

		for a := 0; a < 8; a++ {
			detail = append(detail, paramTarget{element: 0x23, param: byte(a*3 + 0), channel: ch}) // Send ON
			detail = append(detail, paramTarget{element: 0x23, param: byte(a*3 + 1), channel: ch}) // Send PRE/POST
			detail = append(detail, paramTarget{element: 0x23, param: byte(a*3 + 2), channel: ch}) // Send LEVEL
		}
	}

	// Equalizador e Compressor do Stereo Master
	detail = append(detail, generateEQTargets(0x50, 0x00)...)
	detail = append(detail, generateCompTargets(0x51, 0x00)...)

	// Equalizador dos Aux Masters 1 a 8
	for i := 0; i < 8; i++ {
		detail = append(detail, generateEQTargets(0x35, byte(i))...)
	}

	// Equalizador e Compressor dos Canais 1 a 32
	for i := 0; i < 32; i++ {
		ch := byte(i)
		detail = append(detail, generateEQTargets(0x20, ch)...)
		detail = append(detail, generateCompTargets(0x1F, ch)...)
	}

	return critical, detail
}

func generateEQTargets(element, channel byte) []paramTarget {
	return []paramTarget{
		{element: element, param: 0x0F, channel: channel}, // ON/OFF
		{element: element, param: 0x01, channel: channel}, // Low Q/Type
		{element: element, param: 0x02, channel: channel}, // Low Freq
		{element: element, param: 0x03, channel: channel}, // Low Gain
		{element: element, param: 0x04, channel: channel}, // HPF Key
		{element: element, param: 0x05, channel: channel}, // Lo-Mid Q
		{element: element, param: 0x06, channel: channel}, // Lo-Mid Freq
		{element: element, param: 0x07, channel: channel}, // Lo-Mid Gain
		{element: element, param: 0x08, channel: channel}, // Hi-Mid Q
		{element: element, param: 0x09, channel: channel}, // Hi-Mid Freq
		{element: element, param: 0x0A, channel: channel}, // Hi-Mid Gain
		{element: element, param: 0x0B, channel: channel}, // High Q/Type
		{element: element, param: 0x0C, channel: channel}, // High Freq
		{element: element, param: 0x0D, channel: channel}, // High Gain
		{element: element, param: 0x0E, channel: channel}, // LPF Key
	}
}

func generateCompTargets(element, channel byte) []paramTarget {
	return []paramTarget{
		{element: element, param: 0x00, channel: channel}, // Position (Insert)
		{element: element, param: 0x01, channel: channel}, // ON/OFF
		{element: element, param: 0x09, channel: channel}, // Threshold
		{element: element, param: 0x06, channel: channel}, // Ratio
		{element: element, param: 0x04, channel: channel}, // Attack
		{element: element, param: 0x05, channel: channel}, // Release
		{element: element, param: 0x08, channel: channel}, // Knee
		{element: element, param: 0x07, channel: channel}, // Gain
	}
}

func generateGateTargets(element, channel byte) []paramTarget {
	return []paramTarget{
		{element: element, param: 0x00, channel: channel}, // GATE ON/OFF
		{element: element, param: 0x01, channel: channel}, // Threshold
		{element: element, param: 0x02, channel: channel}, // Range
		{element: element, param: 0x03, channel: channel}, // Attack
		{element: element, param: 0x04, channel: channel}, // Hold
		{element: element, param: 0x05, channel: channel}, // Decay
	}
}

// generateMainViewTargets retorna apenas os 114 parâmetros dos controles visíveis da mixagem principal
func generateMainViewTargets() []paramTarget {
	var targets []paramTarget
	// Stereo Master Fader & ON
	targets = append(targets, paramTarget{element: 0x4F, param: 0x00, channel: 0x00})
	targets = append(targets, paramTarget{element: 0x4D, param: 0x00, channel: 0x00})

	// Aux Masters 1 a 8 Fader & ON
	for i := 0; i < 8; i++ {
		auxCh := byte(i)
		targets = append(targets, paramTarget{element: 0x39, param: 0x00, channel: auxCh})
		targets = append(targets, paramTarget{element: 0x36, param: 0x00, channel: auxCh})
	}

	// Canais de Entrada 1 a 32 (Fader, Mute, Pan)
	for i := 0; i < 32; i++ {
		ch := byte(i)
		targets = append(targets, paramTarget{element: 0x1C, param: 0x00, channel: ch})
		targets = append(targets, paramTarget{element: 0x1A, param: 0x00, channel: ch})
		targets = append(targets, paramTarget{element: 0x1B, param: 0x00, channel: ch})
	}
	return targets
}

// generateAuxSendsTargets retorna os envios de todos os 32 canais para um auxiliar específico (96 requisições)
func generateAuxSendsTargets(auxIdx int) []paramTarget {
	var targets []paramTarget
	if auxIdx < 0 || auxIdx > 7 {
		return targets
	}
	for i := 0; i < 32; i++ {
		ch := byte(i)
		targets = append(targets, paramTarget{element: 0x23, param: byte(auxIdx*3 + 0), channel: ch}) // Send ON
		targets = append(targets, paramTarget{element: 0x23, param: byte(auxIdx*3 + 1), channel: ch}) // Send PRE/POST
		targets = append(targets, paramTarget{element: 0x23, param: byte(auxIdx*3 + 2), channel: ch}) // Send LEVEL
	}
	return targets
}

// generateChannelFXTargets retorna EQ, Comp e Gate de um canal ou saída específica (15 a 30 requisições)
func generateChannelFXTargets(targetKey string) []paramTarget {
	var targets []paramTarget
	targetKey = strings.TrimSpace(strings.ToUpper(targetKey))

	if targetKey == "MASTER" || targetKey == "MASTEROUT" {
		targets = append(targets, generateEQTargets(0x50, 0x00)...)
		targets = append(targets, generateCompTargets(0x51, 0x00)...)
		return targets
	}

	if strings.HasPrefix(targetKey, "AUX") {
		numStr := strings.TrimPrefix(targetKey, "AUX")
		auxNum := 0
		fmt.Sscanf(numStr, "%d", &auxNum)
		if auxNum >= 1 && auxNum <= 8 {
			auxCh := byte(auxNum - 1)
			targets = append(targets, generateEQTargets(0x35, auxCh)...)
		}
		return targets
	}

	// Canal numérico 0 a 31 (ou com prefixo "CH")
	cleanStr := strings.TrimPrefix(targetKey, "CH")
	cleanStr = strings.TrimSpace(cleanStr)
	chIdx := -1
	if n, err := strconv.Atoi(cleanStr); err == nil {
		if strings.HasPrefix(targetKey, "CH") {
			chIdx = n - 1
		} else {
			if n >= 0 && n < 32 {
				chIdx = n
			} else if n >= 1 && n <= 32 {
				chIdx = n - 1
			}
		}
	}

	if chIdx >= 0 && chIdx < 32 {
		ch := byte(chIdx)
		targets = append(targets, paramTarget{element: 0x1D, param: 0x00, channel: ch}) // Attenuator
		targets = append(targets, generateEQTargets(0x20, ch)...)
		targets = append(targets, generateCompTargets(0x1F, ch)...)
		targets = append(targets, generateGateTargets(0x21, ch)...)
		// Envios Auxiliares 1 a 8 do canal (ON e Nível)
		for auxIdx := 0; auxIdx < 8; auxIdx++ {
			targets = append(targets, paramTarget{element: 0x23, param: byte(auxIdx * 3), channel: ch})     // Aux ON
			targets = append(targets, paramTarget{element: 0x23, param: byte(auxIdx*3 + 2), channel: ch}) // Aux Level
		}
	}

	return targets
}

// executeSyncQueue executa uma sequência de consultas com pacing e drain window
func executeSyncQueue(targets []paramTarget, taskName string, paceDelay time.Duration, drainWindow time.Duration) (int32, error) {
	midiLock.Lock()
	out := midiOut
	midiLock.Unlock()

	if out == nil {
		log.Printf("[SYNC] Modo simulação: '%s' já sincronizado em memória.", taskName)
		return 0, nil
	}

	if paceDelay < 15*time.Millisecond {
		paceDelay = 15 * time.Millisecond
	}

	startReplies := syncReplyCounter.Load()

	for _, t := range targets {
		reqBytes := Build01V96ParamRequest(t.element, t.param, t.channel)

		midiLock.Lock()
		_ = sendMIDISysExLocked(reqBytes)
		midiLock.Unlock()

		time.Sleep(paceDelay)
	}

	if drainWindow > 0 {
		time.Sleep(drainWindow)
	}

	received := syncReplyCounter.Load() - startReplies
	log.Printf("[SYNC] '%s' finalizado (%d solicitados, %d respostas recebidas da mesa).", taskName, len(targets), received)
	if received == 0 {
		log.Printf("[SYNC] AVISO: Nenhuma resposta SysEx recebida da mesa para '%s'. Verifique MIDI IN, canal MIDI (Rx/Tx = 1) e Parameter Change na 01V96.", taskName)
	}
	return received, nil
}

// RequestMainViewSync sincroniza apenas a tela principal (114 itens: canais 1..32 e saídas)
func RequestMainViewSync(paceDelay time.Duration) error {
	mixerSyncLock.Lock()
	if mixerSyncActive.Load() {
		mixerSyncLock.Unlock()
		return fmt.Errorf("sincronização com a mesa física já em andamento")
	}
	mixerSyncActive.Store(true)
	mixerSyncLock.Unlock()

	defer mixerSyncActive.Store(false)

	targets := generateMainViewTargets()
	mixerSyncTotalReqs.Store(int32(len(targets)))
	mixerSyncProgress.Store(0)

	log.Printf("[SYNC-VIEW] Sincronizando Visão Principal com a Yamaha 01V96 (%d itens, intervalo: %v)...", len(targets), paceDelay)

	running := true
	broadcastWS(WSMessage{
		Type:    "sync_status",
		Running: &running,
		Hex:     "Sincronizando faders e mutes principais...",
	})

	midiLock.Lock()
	hasOut := midiOut != nil
	midiLock.Unlock()

	received, _ := executeSyncQueue(targets, "Controles Principais", paceDelay, 350*time.Millisecond)

	FlushStateToDiskIfDirty()

	stopped := false
	statusMsg := "Mixagem principal sincronizada!"
	if hasOut && received == 0 {
		statusMsg = "Sem resposta da mesa física (verifique MIDI IN / Rx-Tx Ch 1)"
	}
	broadcastWS(WSMessage{
		Type:    "sync_status",
		Running: &stopped,
		Hex:     statusMsg,
	})

	broadcastWS(WSMessage{
		Type: "state_sync",
		Hex:  "Canais sincronizados com a mesa física!",
	})
	return nil
}

// RequestAuxSendsSync sincroniza sob demanda os envios dos 32 canais para o auxiliar selecionado (96 itens)
func RequestAuxSendsSync(auxIdx int, paceDelay time.Duration) error {
	targets := generateAuxSendsTargets(auxIdx)
	if len(targets) == 0 {
		return nil
	}

	log.Printf("[SYNC-AUX] Sincronizando envios de canais para AUX %d (%d itens)...", auxIdx+1, len(targets))
	_, err := executeSyncQueue(targets, fmt.Sprintf("Sends AUX %d", auxIdx+1), paceDelay, 250*time.Millisecond)
	FlushStateToDiskIfDirty()
	return err
}

// RequestChannelFXSync sincroniza sob demanda o rack de efeitos (EQ, Comp, Gate) de um canal específico (15-30 itens)
func RequestChannelFXSync(targetKey string, paceDelay time.Duration) error {
	targets := generateChannelFXTargets(targetKey)
	if len(targets) == 0 {
		return nil
	}

	log.Printf("[SYNC-FX] Sincronizando Rack de Efeitos para '%s' (%d itens)...", targetKey, len(targets))
	_, err := executeSyncQueue(targets, fmt.Sprintf("Rack Efeitos %s", targetKey), paceDelay, 150*time.Millisecond)
	FlushStateToDiskIfDirty()
	return err
}

// RequestMixerInitialState consulta os parâmetros da mixagem principal de forma rápida e segura
func RequestMixerInitialState(paceDelay time.Duration) error {
	return RequestMainViewSync(paceDelay)
}

// ==========================================
// TELEMETRIA SOB DEMANDA: GAIN REDUCTION (0x21)
// ==========================================

var (
	grSubLock         sync.Mutex
	grSubscribers     = make(map[*wsClient]string)
	grPollRunning     bool
	grPollStopChan    chan struct{}
	grPollRefreshChan = make(chan struct{}, 1)

	meterDiagLock sync.Mutex
	meterDiagLast = make(map[uint32]time.Time)
)

// setGRSubscription registra ou remove o interesse de uma tela WebSocket pelo Gain Reduction de um canal/saída.
// O polling MIDI (ou telemetria simulada) só permanece ativo enquanto houver ao menos 1 tela com o compressor aberto.
func setGRSubscription(client *wsClient, target string) {
	if client == nil {
		return
	}

	target = strings.TrimSpace(target)
	grSubLock.Lock()
	if target == "" {
		delete(grSubscribers, client)
	} else {
		grSubscribers[client] = target
	}
	shouldSendStop := reconcileGRPollingLocked()
	grSubLock.Unlock()

	if shouldSendStop {
		midiLock.Lock()
		if midiOut != nil {
			_ = sendMIDISysExLocked(Build01V96MeterStop())
		}
		midiLock.Unlock()
	}

	if target != "" {
		select {
		case grPollRefreshChan <- struct{}{}:
		default:
		}
	}
}

func reconcileGRPollingLocked() (shouldSendStop bool) {
	if len(grSubscribers) > 0 && !grPollRunning {
		grPollRunning = true
		grPollStopChan = make(chan struct{})
		go runGRPoller(grPollStopChan)
		return false
	}

	if len(grSubscribers) == 0 && grPollRunning {
		close(grPollStopChan)
		grPollStopChan = nil
		grPollRunning = false
		return true
	}
	return false
}

func getUniqueGRTargets() []string {
	grSubLock.Lock()
	defer grSubLock.Unlock()

	seen := make(map[string]bool, len(grSubscribers))
	targets := make([]string, 0, len(grSubscribers))
	for _, t := range grSubscribers {
		if t != "" && !seen[t] {
			seen[t] = true
			targets = append(targets, t)
		}
	}
	return targets
}

// sendGRToSubscribers envia o valor de Gain Reduction apenas para as conexões que estão vendo aquele canal/saída.
func sendGRToSubscribers(target string, grDb float64, raw int) {
	grSubLock.Lock()
	var targets []*wsClient
	for c, t := range grSubscribers {
		if t == target {
			targets = append(targets, c)
		}
	}
	grSubLock.Unlock()

	if len(targets) == 0 {
		return
	}

	b, err := json.Marshal(WSMessage{
		Type:   "gr_meter",
		Target: target,
		GRDb:   math.Round(grDb*10) / 10,
		Raw:    raw,
	})
	if err != nil {
		return
	}

	for _, c := range targets {
		c.enqueueBytes(b)
	}
}

// sendGRMeterRequestsForTargets envia pedidos SysEx 0x21 apenas para os canais atualmente abertos em tela.
func sendGRMeterRequestsForTargets(targets []string) {
	midiLock.Lock()
	defer midiLock.Unlock()

	if midiOut == nil {
		return
	}

	for _, t := range targets {
		if chNum, err := strconv.Atoi(t); err == nil && chNum >= 0 && chNum < 32 {
			// Bloco 00 05 [ch] count=1 (canais de entrada 1..32, dinâmica/GR)
			req := Build01V96MeterRequest(0x00, 0x05, byte(chNum), 1)
			_ = sendMIDISysExLocked(req)
		} else if t == "MASTER" {
			req := Build01V96MeterRequest(0x04, 0x00, 0x00, 2)
			_ = sendMIDISysExLocked(req)
		}
	}
}

func runGRPoller(stopChan chan struct{}) {
	midiLock.Lock()
	hasMIDI := midiOut != nil
	midiLock.Unlock()

	if hasMIDI {
		// Na 01V96 cada Meter Request mantém o stream por ~10s; renovamos a cada 8s enquanto houver tela aberta.
		sendGRMeterRequestsForTargets(getUniqueGRTargets())
		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stopChan:
				return
			case <-grPollRefreshChan:
				sendGRMeterRequestsForTargets(getUniqueGRTargets())
			case <-ticker.C:
				targets := getUniqueGRTargets()
				if len(targets) > 0 {
					sendGRMeterRequestsForTargets(targets)
				}
			}
		}
	}

	// Modo simulação (-sim / sem porta física): gera telemetria sintética apenas para canais com tela aberta
	ticker := time.NewTicker(90 * time.Millisecond)
	defer ticker.Stop()

	phase := 0.0
	for {
		select {
		case <-stopChan:
			return
		case <-grPollRefreshChan:
			// Atualiza no próximo tick
		case <-ticker.C:
			phase += 0.25
			targets := getUniqueGRTargets()
			if len(targets) == 0 {
				continue
			}

			state := GetCurrentMixerState()
			for _, t := range targets {
				var compOn int
				var threshold, ratio int

				if chNum, err := strconv.Atoi(t); err == nil && chNum >= 0 && chNum < 32 {
					c := state.Inputs[chNum].Comp
					compOn, threshold, ratio = c.On, c.Threshold, c.Ratio
				} else if t == "MASTER" {
					c := state.StereoMaster.Comp
					compOn, threshold, ratio = c.On, c.Threshold, c.Ratio
				} else if strings.HasPrefix(t, "AUX") {
					if auxNum, err := strconv.Atoi(strings.TrimPrefix(t, "AUX")); err == nil && auxNum >= 1 && auxNum <= 8 {
						c := state.AuxMasters[auxNum-1].Comp
						compOn, threshold, ratio = c.On, c.Threshold, c.Ratio
					}
				}

				if compOn == 0 || ratio == 0 {
					sendGRToSubscribers(t, 0, 0)
					continue
				}

				// Simula redução de ganho proporcional ao threshold/ratio apenas quando o compressor está ativo
				depth := math.Max(0.5, float64(-threshold)/80.0) * (float64(ratio) / 10.0)
				wave := 0.5 + 0.5*math.Sin(phase)*math.Cos(phase*0.37)
				grDb := math.Min(24.0, depth*wave*2.2)
				sendGRToSubscribers(t, grDb, 0)
			}
		}
	}
}

// raw01V96MeterToGRDb converte valor bruto de 14 bits de meter (0x21) em dB de Gain Reduction.
// Nota: escala preliminar (0..32 -> 0..24 dB) sujeita à calibração final na 01V96 física.
func raw01V96MeterToGRDb(raw int) float64 {
	if raw <= 0 {
		return 0
	}
	if raw <= 32 {
		return (float64(raw) / 32.0) * 24.0
	}
	if raw <= 240 {
		return float64(raw) / 10.0
	}
	return math.Min(24.0, (float64(raw)/1023.0)*24.0)
}

func handleIncomingMeterData(ul, lu, ll byte, values []int, rawBytes []byte) {
	// Log de diagnóstico limitado a 1x a cada 500ms por endereço para facilitar calibração na mesa física
	addrKey := (uint32(ul) << 16) | (uint32(lu) << 8) | uint32(ll)
	now := time.Now()
	meterDiagLock.Lock()
	lastLog, seen := meterDiagLast[addrKey]
	shouldLog := !seen || now.Sub(lastLog) >= 500*time.Millisecond
	if shouldLog {
		meterDiagLast[addrKey] = now
	}
	meterDiagLock.Unlock()

	if shouldLog {
		log.Printf("MIDI IN -> Remote Meter 0x21 [UL=0x%02X LU=0x%02X LL=0x%02X] values=%v (raw=% X)", ul, lu, ll, values, rawBytes)
	}

	// Mapeia bloco de canais (UL=0x00, LU=0x05) apenas para quem está com a tela daquele canal aberta
	if ul == 0x00 && lu == 0x05 {
		for i, raw := range values {
			ch := int(ll) + i
			if ch >= 0 && ch < 32 {
				target := strconv.Itoa(ch)
				sendGRToSubscribers(target, raw01V96MeterToGRDb(raw), raw)
			}
		}
	}
}
