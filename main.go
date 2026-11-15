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

type ParamCommand struct {
	Element byte `json:"element"` // ex: 0x1C (Fader), 0x1A (Mute/ON)
	Param   byte `json:"param"`   // ex: 0x00
	Channel byte `json:"channel"` // ex: 0x00 (Canal 1)
	Value   int  `json:"value"`   // ex: 0 a 1023
}

type WSResponse struct {
	Type string `json:"type,omitempty"`
	Port string `json:"port,omitempty"`
	Hex  string `json:"hex"`
}

var (
	midiOut      drivers.Out
	midiPortName string
	midiLock     sync.Mutex
	upgrader     = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

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
	defer conn.Close()
	log.Println("Cliente WebSocket conectado:", r.RemoteAddr)

	midiLock.Lock()
	pName := midiPortName
	midiLock.Unlock()

	// Envia informação da porta conectada logo após conectar
	conn.WriteJSON(WSResponse{
		Type: "init",
		Port: pName,
		Hex:  "Aguardando comandos...",
	})

	for {
		var cmd ParamCommand
		if err := conn.ReadJSON(&cmd); err != nil {
			log.Println("Cliente WebSocket desconectado:", err)
			break
		}

		sysex := Build01V96SysEx(cmd.Element, cmd.Param, cmd.Channel, cmd.Value)
		hexStr := fmt.Sprintf("% X", sysex)

		// Log no terminal para conferência
		fmt.Printf("WS -> SysEx: %s\n", hexStr)

		// Envia para a saída MIDI
		midiLock.Lock()
		if midiOut != nil {
			if err := midiOut.Send(sysex); err != nil {
				log.Printf("Erro ao enviar MIDI: %v", err)
			}
		}
		midiLock.Unlock()

		// Devolve a string hexadecimal formada para exibir na tela
		conn.WriteJSON(WSResponse{
			Type: "sysex",
			Port: pName,
			Hex:  hexStr,
		})
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

// runFaderSimulation executa a simulação contínua e realista de movimentação de faders nos canais 1 a 8,
// enviando as mensagens SysEx da 01V96 diretamente na porta MIDI selecionada sem iniciar o servidor WebSocket.
func runFaderSimulation(out drivers.Out, portName string) {
	fmt.Println("\n==============================================================")
	fmt.Println("  Yamaha 01V96 — Simulador de Operação Real (Faders Ch 1-8)")
	fmt.Println("==============================================================")
	fmt.Printf("Porta MIDI Selecionada: %s\n", portName)
	fmt.Println("Servidor WebSocket: DESATIVADO")
	fmt.Println("Parâmetro: Element 0x1C (Input Fader), Param 0x00, Canais 0x00..0x07")
	fmt.Println("Pressione Ctrl+C para encerrar a simulação a qualquer momento.")
	fmt.Println("--------------------------------------------------------------")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	const numChannels = 8
	channels := make([]faderChannelState, numChannels)

	// Inicializa os 8 canais com posições operacionais típicas (próximas de 0.0 dB / 823)
	for i := 0; i < numChannels; i++ {
		initVal := 650 + r.Intn(200)
		channels[i] = faderChannelState{
			currentVal: initVal,
			targetVal:  initVal,
			holdTicks:  r.Intn(15),
		}
	}

	// Ciclo de atualização de 40ms (~25 FPS de animação/motor)
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			fmt.Println("\n[!] Interrupção recebida (Ctrl+C). Finalizando simulação de faders...")
			return
		case <-ticker.C:
			for ch := 0; ch < numChannels; ch++ {
				st := &channels[ch]

				// Se o fader já está no alvo
				if st.currentVal == st.targetVal {
					if st.holdTicks > 0 {
						st.holdTicks--
						continue
					}

					// Probabilidade de iniciar novo ajuste (simula operador alternando entre os faders)
					if r.Float32() < 0.06 {
						dice := r.Float32()
						var newTarget int
						if dice < 0.05 {
							// Fade out total (-∞ dB)
							newTarget = 0
						} else if dice > 0.95 {
							// Boost alto (+8.0 a +10.0 dB)
							newTarget = 980 + r.Intn(44)
						} else {
							// Ajustes comuns de mixagem ao redor da posição atual (-150 a +150 pontos)
							delta := r.Intn(300) - 150
							newTarget = st.currentVal + delta
							if newTarget < 250 {
								newTarget = 250 + r.Intn(150)
							}
							if newTarget > 950 {
								newTarget = 950
							}
						}
						st.targetVal = newTarget
					}
					continue
				}

				// Move gradualmente em direção ao target (passo suave de 5 a 12 unidades por ciclo)
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

				// Ao atingir o alvo, mantém uma pausa simulando operador soltando o fader
				if st.currentVal == st.targetVal {
					st.holdTicks = 12 + r.Intn(60) // 0.5s a 2.5s parado
				}

				// Element: 0x1C (Input Fader), Param: 0x00, Channel: 0x00 a 0x07 (Ch 1 a 8)
				sysex := Build01V96SysEx(0x1C, 0x00, byte(ch), st.currentVal)

				// Envia para a porta MIDI física selecionada
				midiLock.Lock()
				if out != nil {
					if err := out.Send(sysex); err != nil {
						log.Printf("Erro ao enviar SysEx MIDI no Canal %d: %v", ch+1, err)
					}
				}
				midiLock.Unlock()

				dbStr := faderValueToDB(st.currentVal)
				fmt.Printf("[Ch %d] Fader: %4d (%8s) | SysEx: % X\n", ch+1, st.currentVal, dbStr, sysex)
			}
		}
	}
}

func main() {
	flagPort := flag.String("port", "", "Porta MIDI de saída por índice ou nome (ex: -port 1 ou -port Delta). Deixe vazio para escolher interativamente.")
	flagAddr := flag.String("addr", ":8080", "Endereço HTTP do servidor")
	flagSimulate := flag.Bool("simulate", false, "Simula operação real da Yamaha 01V96 enviando dados aleatórios de fader (canais 1 a 8) na porta MIDI selecionada sem subir o servidor WS")
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

	// Se o modo de simulação de operação foi ativado, executa o loop de faders e não sobe o WS
	if *flagSimulate {
		runFaderSimulation(midiOut, midiPortName)
		return
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index.html")
	})
	http.HandleFunc("/ws", handleWS)

	log.Printf("Servidor WebSocket rodando em http://localhost%s", *flagAddr)
	log.Fatal(http.ListenAndServe(*flagAddr, nil))
}
