package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"

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
	Hex string `json:"hex"`
}

var (
	midiOut  drivers.Out
	midiLock sync.Mutex
	upgrader = websocket.Upgrader{
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
		conn.WriteJSON(WSResponse{Hex: hexStr})
	}
}

func main() {
	defer midi.CloseDriver()

	outs := midi.GetOutPorts()
	if len(outs) > 0 {
		midiOut = outs[0] // <-- Faltava o índice [0] aqui
		if err := midiOut.Open(); err != nil {
			log.Printf("Aviso: não foi possível abrir %s: %v", midiOut.String(), err)
		} else {
			defer midiOut.Close()
			log.Printf("Conectado na saída MIDI: %s", midiOut.String())
		}
	} else {
		log.Println("Nenhuma porta MIDI encontrada (rodando apenas em modo log).")
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index.html")
	})
	http.HandleFunc("/ws", handleWS)

	addr := ":8080"
	log.Printf("Servidor WebSocket rodando em http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
