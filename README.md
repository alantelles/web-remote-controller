# Web Remote Controller — Yamaha 01V96 / 01V96V2

Controle remoto web em tempo real (FOH e monitores/auxiliares) para a mesa de som digital **Yamaha 01V96 / 01V96V2**, comunicando-se via mensagens nativas **MIDI System Exclusive (SysEx)** em portas ALSA do Linux.

---

## Principais Recursos

- **Console Web Completo (`/`):** Faders, Mute/ON, Pan, *Sends on Fader* (Auxiliares 1–8), Equalizador paramétrico de 4 bandas com gráfico em Canvas, Compressor, Noise Gate e medidores em tempo real.
- **Interface Mobile (`/m` ou `/mobile`):** Layout compacto otimizado para smartphones.
- **Personal Mix (`/mix`):** Interface dedicada para músicos/operadores ajustarem seus próprios envios de monitoração.
- **Sincronização Bidirecional:** Espelhamento de estado em tempo real entre a mesa física e os clientes conectados via WebSocket, com *throttling* para proteger o barramento MIDI.
- **Gerenciamento em Software (`data/`):**
  - **Cenas (`data/scenes/`):** Salvar, carregar (*recall*), importar e exportar cenas.
  - **Presets de Canal (`data/channel_presets/`):** Presets de fábrica e de usuário (EQ, Compressor e Gate).
  - **Mute Groups e Personal Mixes:** Persistência local em JSON sem depender ou sobrescrever as bibliotecas internas da mesa física.
- **Autenticação e Permissões:** Controle de acesso via cookie de sessão (`sessions.json`) e usuários configuráveis em `usuarios.csv` (com alteração de senha em `/change-password`).
- **Modo Simulação (`-sim`):** Permite testar todas as interfaces e fluxos sem uma mesa física conectada.

---

## Stack e Arquitetura

- **Backend:** Go (`go 1.24+`) com [`gitlab.com/gomidi/midi/v2`](https://gitlab.com/gomidi/midi/v2) (ALSA `rtmididrv`) e [`github.com/gorilla/websocket`](https://github.com/gorilla/websocket).
- **Frontend:** SPA em HTML5, CSS3 e Vanilla JavaScript puro (sem `npm`, `node_modules` ou etapa de build).

---

## Requisitos

- Linux com suporte a **ALSA** (para comunicação MIDI com a mesa)
- **Go 1.24+** (para compilar ou rodar via `go run`)
- Dependências de sistema para `rtmididrv` / ALSA (veja `install_dependencies.sh`)

---

## Como Executar

### Execução Rápida

```bash
# Menu interativo para escolha da porta MIDI
go run .

# Especificando a porta MIDI por índice ou nome
go run . -port 1
go run . -port "Fast Track"

# Modo de simulação (sem hardware conectado)
go run . -sim -addr :9553
```

### Opções de Linha de Comando

| Flag | Padrão | Descrição |
|---|---|---|
| `-port` | `""` | Porta MIDI de saída por índice ou nome (vazio abre menu interativo). |
| `-addr` | `:9553` | Endereço/porta do servidor HTTP. |
| `-simulate`, `-sim` | `false` | Inicia o simulador de faders (canais 1 a 8) sem porta MIDI física. |
| `-users`, `-users-file` | `usuarios.csv` | Caminho do arquivo CSV de usuários e permissões. |
| `-sessions` | `sessions.json` | Caminho do arquivo de persistência de sessões. |
| `-data` | `data` | Diretório de persistência de cenas, presets, grupos e estado. |
| `-podeDesligarServidor` | `false` | Habilita o botão administrativo de desligamento da máquina (`poweroff`). |

### Compilação

```bash
go build -o 01v96-controller .
./01v96-controller -port 1
```

Para build de compatibilidade (Ubuntu 20.04), consulte `Dockerfile.ubuntu2004` e `build-ubuntu2004.sh`.

---

## Testes

```bash
go test -v ./...
```

---

## Deploy em Produção (Systemd + Nginx)

- Serviço systemd de exemplo: [`01v96-controller.service`](01v96-controller.service)
- Configurações de proxy reverso e WebSocket: [`nginx/`](nginx/README.md)

---

## Documentação Adicional

- [`DOCUMENTACAO_PROJETO.md`](DOCUMENTACAO_PROJETO.md) — Arquitetura, decisões técnicas e histórico do projeto.
- [`PARAMETROS_SYSEX_01V96.md`](PARAMETROS_SYSEX_01V96.md) — Especificação técnica dos endereços e escalas SysEx da Yamaha 01V96.

---

## Licença

Este projeto é distribuído sob a **[PolyForm Noncommercial License 1.0.0](LICENSE)** (`PolyForm-Noncommercial-1.0.0`).

- **Permitido:** Uso, cópia, modificação e distribuição gratuitos para fins **exclusivamente não comerciais** (uso pessoal, igrejas, instituições sem fins lucrativos, estudo).
- **Proibido:** Qualquer uso comercial, venda do software ou utilização do código para criação de produtos ou serviços destinados à venda.
- **Créditos obrigatórios:** Qualquer redistribuição do software (ou de partes dele) deve manter os termos da licença e a linha `Required Notice:` presente no arquivo [`LICENSE`](LICENSE).
