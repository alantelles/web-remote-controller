package main

import (
	"bufio"
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

func handleWS(w http.ResponseWriter, r *http.Request) {
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

	log.Println("Cliente WebSocket conectado:", r.RemoteAddr)

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
		sysex := Build01V96SysEx(msg.Element, msg.Param, msg.Channel, msg.Value)
		hexStr := fmt.Sprintf("% X", sysex)

		// Log detalhado no terminal identificando a saída / destino e parâmetro
		var targetDesc string
		chNum := msg.Channel + 1
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
			pName, ok := eqParamNames[msg.Param]
			if !ok {
				pName = fmt.Sprintf("Param 0x%02X", msg.Param)
			}
			targetDesc = fmt.Sprintf("CH %02d -> EQ %s: %d", chNum, pName, msg.Value)
		default:
			targetDesc = fmt.Sprintf("CH %02d -> Element 0x%02X, Param 0x%02X: %d", chNum, msg.Element, msg.Param, msg.Value)
		}

		fmt.Printf("WS -> SysEx: %s | %s\n", hexStr, targetDesc)

		// Aplica throttle antes de enviar ao barramento MIDI e ao WebSocket.
		// Eventos ON/MUTE/Bypass discretos passam direto sem throttling
		isDiscrete := msg.Element == 0x1A ||
			(msg.Element == 0x23 && msg.Param >= 0x08) ||
			(msg.Element == 0x20 && (msg.Param == 0x00 || msg.Param == 0x0D || msg.Param == 0x0E))
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
	flag.Parse()

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

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
		os.Exit(0)
	}()

	log.Printf("Servidor Console Web rodando em http://localhost%s", *flagAddr)
	log.Fatal(http.ListenAndServe(*flagAddr, nil))
}
