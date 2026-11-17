# GEMINI.md — Guia de Contexto e Utilidades para Novas Sessões

Este documento serve como ponto de partida rápido para novas sessões de IA ou desenvolvedores trabalhando no repositório **`web-remote-controller`**.

---

## 1. Visão Geral do Repositório

* **Objetivo:** Controle remoto web em tempo real (FOH e monitores/auxiliares) para a mesa de som digital **Yamaha 01V96 / 01V96V2**.
* **Comunicação com a Mesa:** Mensagens nativas MIDI System Exclusive (SysEx de 14 bytes — *Parameter Change*) transmitidas diretamente para portas ALSA do Linux.
* **Stack:**
  * **Backend:** Go (Go 1.20+) usando `gitlab.com/gomidi/midi/v2` (backend ALSA `rtmididrv`) e `github.com/gorilla/websocket`.
  * **Frontend:** Single Page Application (SPA) em HTML5, CSS3 e Vanilla JavaScript puro (zero dependência de `npm`, `node_modules` ou ferramentas de empacotamento).
  * **Autenticação:** Cookie seguro de sessão HttpOnly (`remote_session`) autenticado contra `usuarios.csv`.

---

## 2. Estrutura de Arquivos Principais

| Arquivo | Descrição |
|---|---|
| `main.go` | Ponto de entrada do backend. Gerencia portas MIDI (ALSA), servidor HTTP, conexões WebSocket, throttling de pacotes, autenticação por cookies e simulador de faders. |
| `index.html` | Interface gráfica do console. Faders estilizados, rotas de Auxiliares (*Sends on Fader*), modal de Equalizador 4 bandas com gráfico Canvas, modal de Compressor e medidores. |
| `login.html` | Interface de autenticação com suporte a login por formulário padrão e requisições assíncronas JSON. |
| `usuarios.csv` | Banco de dados plano de usuários (`nome,usuario,senha,papel`). Relido em tempo real sem necessidade de reiniciar o binário. |
| `auth_test.go` | Suíte de testes automatizados para verificação do módulo de autenticação e manipulação do CSV. |
| `DOCUMENTACAO_PROJETO.md` | Documento consolidado com arquitetura, o que já foi feito, decisões e roadmap. |
| `PARAMETROS_SYSEX_01V96.md` | Especificação técnica detalhada de cada Elemento, Parâmetro, Canal e escalas de conversão (dB, Hz, Q). |

---

## 3. Comandos Úteis e Execução

### Como Executar o Servidor
```bash
# Execução padrão (abre menu interativo de seleção de porta MIDI no terminal)
go run .

# Execução direta com porta específica (ex: porta 1 ou busca por nome)
go run . -port 1
go run . -port "Fast Track"

# Execução em Modo de Simulação (sem porta física, ideal para testar telas sem a mesa)
go run . -sim
go run . -port sim -addr :8080

# Usando arquivo alternativo de usuários
go run . -users /caminho/para/usuarios.csv
```

### Rodar os Testes Automatizados
```bash
go test -v ./...
```

### Diagnóstico e Inspeção MIDI no Linux (ALSA)
```bash
# Listar portas MIDI de hardware e clientes ALSA
aconnect -l
amidi -l

# Monitorar em tempo real mensagens MIDI transmitidas em uma porta (ex: cliente 20:0)
aseqdump -p 20:0

# Carregar módulo de portas MIDI virtuais no kernel Linux (para testes com Studio Manager/Wine)
sudo modprobe snd-virmidi
```

---

## 4. Regras de Arquitetura e Boas Práticas

1. **Frontend Leve e Sem Build Step:**
   * **Não introduzir `npm`, `webpack`, `react` ou `vite`** no frontend, a menos que solicitado expressamente pelo usuário. Manter tudo em Vanilla JS/CSS garante facilidade de deploy em sistemas operacionais embarcados ou servidores leves.
2. **Respeito ao Barramento MIDI e Throttling:**
   * A taxa de transmissão física do MIDI é de **31.250 bauds**. Mover faders contínuos pode saturar o barramento e causar latência.
   * Toda mensagem de fader ou knob **deve passar pelo `paramThrottler`** (20 ms para MIDI, 16 ms para WebSocket).
   * Mensagens discretas críticas (botões ON/MUTE, seletores de modo, botões de bypass) **não devem sofrer throttle** para responderem instantaneamente ao clique do usuário.
3. **Padrão do SysEx Yamaha 01V96:**
   * Toda mensagem de edição em tempo real tem exatamente **14 bytes**:
     `F0 43 10 3E 0D 01 [ee] [pp] [cc] 00 00 [dd3] [dd4] F7`
   * `dd3 = (value >> 7) & 0x7F` e `dd4 = value & 0x7F`.
   * Para consultar endereços de parâmetros específicos, consulte [PARAMETROS_SYSEX_01V96.md](file:///home/alantelles/develop/web-remote-controller/PARAMETROS_SYSEX_01V96.md).
4. **Segurança de Sessão:**
   * Senhas nunca devem ser trafegadas nos endpoints de consulta (ex: campo `Password` marcado com `json:"-"` na struct `User`).
   * A checagem de autenticação no WebSocket deve ser feita antes do upgrade da conexão HTTP.

---

## 5. Dicionário Rápido de Elementos SysEx (Cheat Sheet)

* `0x1C`: Input Fader (Canal 1 a 32, cc = 0 a 31, valor 0 a 1023)
* `0x1A`: Input ON / MUTE (0 = Mute, 1 = On)
* `0x1B`: Input PAN (0 = L63, 63 = C, 126 = R63)
* `0x20`: Input Equalizer 4 Bandas (Param 0 a 14)
* `0x22`: Input Compressor (Param 0 a 6)
* `0x23`: Input Aux Send (`pp 0..7`: Níveis Aux 1..8; `pp 8..15`: ON Aux 1..8)
* `0x31`: Aux Master Fader (Aux 1 a 8, cc = 0 a 7)
* `0x2F`: Aux Master ON / MUTE
* `0x35`: Aux Master Equalizer
* `0x36`: Aux Master Compressor
* `0x4D`: Stereo Master Out Fader
* `0x4B`: Stereo Master Out ON / MUTE
* `0x50`: Stereo Master Equalizer
* `0x51`: Stereo Master Compressor
