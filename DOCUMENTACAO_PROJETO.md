# Documentação do Projeto: Web Remote Controller (Yamaha 01V96V2)

> **Status:** Em desenvolvimento ativo (v1.0 Funcional com Simulação e Autenticação)  
> **Console Alvo:** Yamaha 01V96 Version 2 (01V96V2)  
> **Comunicação:** MIDI Nativo via ALSA (Linux) / System Exclusive (SysEx)  
> **Arquitetura:** Go (Backend HTTP + WebSocket) + SPA HTML5/CSS3/Vanilla JS (Frontend)

---

## 1. Visão Geral e Propósito

O **Web Remote Controller** é uma solução moderna e leve para controle remoto em tempo real da mesa de som digital **Yamaha 01V96 / 01V96V2**.

### O Problema que Resolve
* O software oficial da Yamaha (*Studio Manager* + *01V96 Editor*) foi projetado para versões antigas de Windows (32-bit), com interface datada, sem suporte nativo a múltiplos dispositivos móveis na rede e com alta complexidade de execução no Linux (dependência de Wine e roteamento de drivers MIDI legados).
* A necessidade prática em eventos, cultos e gravações é permitir que músicos ajustem seus próprios monitores de palco (fones/auxiliares) e que operadores tenham mobilidade total com tablets ou celulares, sem restrições de sistema operacional ou instalações de aplicativos pesados.

### A Solução Implementada
* **Servidor em Go:** Executável leve, de alta performance e baixo consumo de recursos, rodando nativamente no Linux (Ubuntu / Lubuntu / Debian) conectado diretamente à mesa via USB ou cabos MIDI DIN (interface USB-MIDI).
* **Interface Web Responsiva:** Aplicação web embutida, acessível de qualquer navegador em smartphone, tablet ou computador na rede local sem requerer instalação de drivers ou HTTPS.
* **Comunicação Bidirecional via WebSocket:** Atualização instantânea com taxa de atualização ultra fluida, sincronizando todos os clientes conectados.
* **Protocolo SysEx Nativo:** Uso direto da especificação oficial *Yamaha Parameter Change*, permitindo o controle paramétrico granular de canais, mixagens auxiliares, equalizadores e compressores.

---

## 2. O Que Fizemos Até Aqui (Histórico de Desenvolvimento)

### Etapa 1: Engenharia Reversa e Validação do Protocolo SysEx
* **Investigação do Protocolo:** Mapeamento da estrutura de 14 bytes do SysEx de alteração de parâmetros em tempo real (*Edit Buffer*) da Yamaha 01V96V2 (`F0 43 1n 3E 0D 01 [ee] [pp] [cc] [dd1] [dd2] [dd3] [dd4] F7`).
* **Validação de Hardware no Linux:**
  * Configuração e testes com interfaces MIDI reconhecidas pelo ALSA (como *M-Audio Fast Track Pro* e *M-Audio Delta 1010*).
  * Criação de portas virtuais com `snd-virmidi` e monitoramento de pacotes usando `aseqdump` e `amidi`.
  * Validação ponta a ponta com o *Studio Manager* rodando via Wine para certificar o comportamento dos faders e botões.

### Etapa 2: Backend em Go e Camada de Comunicação
* **Driver MIDI em Go:** Integração com `gitlab.com/gomidi/midi/v2` utilizando o backend `rtmididrv` sobre ALSA, permitindo abertura estável de portas MIDI físicas ou virtuais.
* **Seleção Interativa e por Linha de Comando:** Suporte a flags (`-port`, `-addr`, `-sim`, `-users`) e menu de seleção interativa de portas MIDI no terminal no momento do boot.
* **Servidor HTTP e WebSocket:** Roteamento de alto desempenho com `gorilla/websocket` gerenciando conexões simultâneas com broadcast para todos os clientes ativos.

### Etapa 3: Interface Web e Experiência do Usuário (UI/UX)
* **Design de Mesa Digital:** Interface inspirada nos consoles modernos, com faders deslizantes de alta sensibilidade, botões iluminados de `ON/MUTE`, `SOLO`, e `SEL`.
* **Escala de Decibéis (dB) Calibrada:** Mapeamento não linear de 0 a 1023 convertido para a escala oficial da Yamaha (-∞ dB até +10 dB, com ponto 0.0 dB em 823).
* **Suporte a 32 Canais de Entrada:** Estrutura flexível para exibição e controle de até 32 canais de entrada, além do Master Stereo e Auxiliares Masters.
* **Seleção Dinâmica de Canais Visíveis:** Filtro por blocos (Canais 1-8, 9-16, 17-24, 25-32 ou customizados) persistido no `localStorage`.

### Etapa 4: Sends on Faders (Mixagens de Auxiliares)
* **Chaveamento Rápido de Mix:** Seletor de destino de mixagem permitindo operar tanto o **MASTER Stereo** quanto os **AUX 1 a 8** no mesmo layout de faders.
* **Indicação Visual de Roteamento:** Mudança clara na paleta de cores e cabeçalho ao operar em modo Auxiliar, mostrando para qual saída física o sinal está sendo enviado.

### Etapa 5: Equalizador Paramétrico Gráfico (4 Bandas + Filtros)
* **Painel Completo de EQ:**
  * Banda LOW (Q / Shelving / HPF, Frequência de 20 Hz a 20 kHz, Ganho de -18 dB a +18 dB).
  * Banda LO-MID (Q de 10.0 a 0.10, Frequência, Ganho).
  * Banda HI-MID (Q de 10.0 a 0.10, Frequência, Ganho).
  * Banda HIGH (Q / Shelving / LPF, Frequência, Ganho).
  * Filtros de corte passa-altas (HPF) e passa-baixas (LPF).
* **Visualização Gráfica Interativa (Canvas):** Renderização matemática em tempo real da curva de resposta em frequência combinada das 4 bandas.
* **Disponibilidade Global:** Implementado para Canais 1 a 32 (Element `0x20`), Auxiliares 1 a 8 (Element `0x35`) e Master Stereo (Element `0x50`).

### Etapa 6: Processador de Dinâmica / Compressor Completo
* **Painel de Compressor:**
  * Parâmetros: Threshold (-54 dB a 0 dB), Ratio (1.0:1 a ∞:1), Attack (0 a 120 ms), Release (5 ms a 42.3 s), Knee (Hard até Soft 5) e Output Gain (0 a +18 dB).
  * Curva de transferência dinâmica desenhada em Canvas interativo.
  * Presets inteligentes rápidos (Vocal, Bateria, Baixo, Master Limiter).
  * Medidor de redução de ganho (Gain Reduction - GR).
* **Disponibilidade Global:** Implementado para Canais 1 a 32 (Element `0x22`), Auxiliares 1 a 8 (Element `0x36`) e Master Stereo (Element `0x51`).

### Etapa 7: Sistema de Autenticação e Perfis de Usuário
* **Segurança Multi-usuário:** Autenticação baseada em sessões seguras criptográficas via cookie `remote_session` (validade de 24 horas, proteção HttpOnly).
* **Gerenciamento Desacoplado via CSV:** Arquivo `usuarios.csv` contendo `nome,usuario,senha,papel`. O arquivo é relido em tempo de execução sem necessitar reiniciar o servidor.
* **Telas Dedicadas:** Página de login profissional (`login.html`), suporte a login via formulário e API REST JSON, rota `/logout` e endpoint `/api/me`.
* **Suíte de Testes Automatizados:** Testes unitários completos cobrindo autenticação, leitura de CSV e sessões em `auth_test.go`.

### Etapa 8: Motor de Simulação e Controle de Tráfego (Throttling)
* **Simulador Multi-canal e Multi-mixagem:** Goroutine interna no Go que simula a movimentação simultânea e realista de faders nos Canais 1 a 8 em todas as 9 mixagens (Master + 8 Auxiliares), permitindo testes de UI e carga mesmo sem a mesa física conectada.
* **Controle da Simulação pela Interface Web:** Botão na barra superior que permite alternar a simulação via mensagem WebSocket `toggle_sim`.
* **Throttler Paramétrico Thread-Safe:**
  * O barramento MIDI opera a 31.250 bauds e satura facilmente com múltiplos faders sendo movidos.
  * Implementado `paramThrottler` com rastreamento por tupla `(Element, Param, Channel)`.
  * Intervalo configurado: 20 ms para envios MIDI e 16 ms para broadcasts WebSocket.
  * Comandos discretos críticos (como ON/MUTE, Bypass e seletores) ignoram o throttling e têm entrega imediata garantida.

### Etapa 9: Interface Mobile Vertical Reduzida (`/m` e `/mobile`)
* **Rota Dedicada:** Endpoints `/m` e `/mobile` servindo `mobile.html`, permitindo operação ergonômica com o polegar em smartphones na vertical.
* **Header Mínimo:** Botão de perfil `👤` (abre menu com papel e logout) e indicador de conexão MIDI/WebSocket 100% estático (sem animações ou pulsações).
* **Redirecionamento Automático Mobile:** Detecção inteligente via User-Agent e dimensões de tela tanto no login quanto na rota raiz (`/`), encaminhando celulares diretamente para `/m` (com parâmetro `?view=desktop` para alternar intencionalmente).
* **Matriz de Saídas Compacta:**
  * Linha 1: Dividida em **3/4** para o botão `MASTER` e **1/4** para o botão `🎚️ OUT` (toggle para exibir/ocultar o fader de saída, persistido no navegador).
  * Linhas 2 e 3: Grades 4x1 com botões de AUX 1 a 4 e AUX 5 a 8.
* **Barra de Funções Retrátil (Colapsável):** Barra recolhível com resumo de canais ativos, liberando espaço vertical na tela para faders.
* **Faders Horizontais em Cartões:**
  * Glow interno e borda superior luminosa em **vermelho** (modo Master) ou **azul** (modo Auxiliar) indicando a rota de mixagem em cada canal.
  * Master da saída selecionada com acabamento translúcido (vermelho no Stereo Master e azul no Auxiliar).
  * Canais de entrada: Nome, botão `ON/MUTE`, botão `SEL` (brilho dourado indicando se EQ/Compressor está ativo).
  * Slider horizontal touch-friendly com manopla ampla, indicador numérico em dB e botões de pulo rápido `[ 0 ]` e `[ -∞ ]`.

---

## 3. Em que Ponto Estamos Hoje (Status Atual)

```
+-------------------------------------------------------------------------------+
|                             ARQUITETURA ATUAL                                 |
+-------------------------------------------------------------------------------+
|                                                                               |
|   +-----------------------+                    +--------------------------+   |
|   |  Dispositivo Cliente  |                    |  Dispositivo Músico      |   |
|   |  (Tablet / Celular)   |                    |  (Smartphone / Web)      |   |
|   +-----------+-----------+                    +------------+-------------+   |
|               |                                             |                 |
|               +--------------------+   +--------------------+                 |
|                                    |   |                                      |
|                                    v   v                                      |
|                            [ HTTP / WebSocket ]                               |
|                                    |                                          |
|                                    v                                          |
|                 +--------------------------------------+                      |
|                 |       Servidor Go (Linux/ALSA)       |                      |
|                 |  - Session Auth (usuarios.csv)       |                      |
|                 |  - Param Throttler (20ms MIDI/16msWS)|                      |
|                 |  - Simulador Multi-Mix Faders        |                      |
|                 +------------------+-------------------+                      |
|                                    |                                          |
|                     [ SysEx 14-byte Parameter Change ]                        |
|                                    |                                          |
|                                    v                                          |
|                  +-----------------------------------+                        |
|                  |     Interface ALSA / Hardware     |                        |
|                  | (Fast Track / USB MIDI / snd-vir) |                        |
|                  +-----------------+-----------------+                        |
|                                    |                                          |
|                                    v                                          |
|                  +-----------------------------------+                        |
|                  |    Mesa Yamaha 01V96 / 01V96V2    |                        |
|                  +-----------------------------------+                        |
+-------------------------------------------------------------------------------+
```

### Funcionalidades Prontas e Operacionais:
1. **Controle de Volumes e Mutes:** Totalmente funcional em Canais 1-32, Stereo Master e Aux Masters 1-8.
2. **Sends on Fader (Monitores):** Comutação suave e envio de SysEx dedicado para Aux 1 a 8.
3. **Equalizador 4 Bandas Paramétrico:** Ajustes finos de frequências, ganhos, Q e filtros com visualizador gráfico.
4. **Compressor Dinâmico:** Ajuste completo de dinâmica com presets e curva de resposta.
5. **Autenticação:** Proteção de acesso com cookies de sessão e usuários configuráveis via CSV.
6. **Ambiente de Desenvolvimento Sem Mesa:** Simulador multi-fader e logs detalhados de decodificação SysEx no console.
7. **Estabilidade de Tráfego:** Throttling ativo protegendo a saída MIDI contra estouro de buffer.

---

## 4. Decisões Técnicas e Arquiteturais Tomadas

| Decisão Tomada | Alternativa Considerada | Motivo da Escolha |
|---|---|---|
| **SysEx Parameter Change** | MIDI Control Change (CC) | O uso de CCs comuns exigiria remapear manualmente a tabela interna de MIDI da 01V96 (que é limitada e não cobre todos os parâmetros e auxiliares). SysEx dá acesso direto e incondicional a 100% da mesa. |
| **Go no Backend** | Node.js ou Python | Go produz um binário estático único sem dependências externas, consome menos de 30 MB de RAM, inicia instantaneamente e tem excelente suporte a concorrência com Goroutines e ALSA nativo. |
| **HTML5/Vanilla JS no Frontend** | Angular / React / Vue | O projeto dispensou ferramentas de *bundling* (`npm`, `webpack`, `vite`), permitindo deploy instantâneo apenas substituindo arquivos estáticos e rodando em navegadores antigos ou embarcados sem overhead. |
| **WebSocket para Transporte** | Web MIDI API do navegador | A Web MIDI API no navegador exige contexto HTTPS obrigatório, drivers locais no cliente e é bloqueada na maioria dos navegadores mobile (ex: Safari iOS). WebSocket delega o acesso físico exclusivamente ao servidor Linux. |
| **Throttling Paramétrico Seletivo** | Throttling global linear | Limitar globalmente causava atrasos em botões de MUTE quando um fader estava se movendo. O rastreamento por tupla `(Element, Param, Channel)` permite que botões discretos passem imediatamente e apenas o fader ativo sofra descarte de quadros repetidos. |
| **Autenticação em CSV com Leitura Dinâmica** | SQLite / Banco de Dados SQL | Um arquivo CSV simples permite fácil edição direta no terminal (`nano usuarios.csv`) sem necessidade de instalar dependências de banco de dados no sistema operacional embarcado. |

---

## 5. Planos de Futuro e Roadmap Prioritário

### Curto Prazo (v1.1)
1. **Feedback Bidirecional (MIDI IN -> WebSocket):**
   * Implementação de goroutine de escuta na porta MIDI IN para receber eventos da mesa física (quando alguém move um fader físico na 01V96) e disparar o broadcast WebSocket atualizando a interface web de todos os dispositivos.
2. **Sincronização Inicial de Estado (Bulk Request / Parameter Request):**
   * Envio de mensagens de solicitação de parâmetros (`F0 43 30 3E 0D ...`) ao iniciar a conexão ou conectar um novo cliente, para ler os valores reais da mesa em vez de assumir valores padrão.
3. **Nomes Personalizados de Canais:**
   * Permitir que o usuário nomeie os canais na interface (ex: "Voz Principal", "Violão", "Bumbo") e salve localmente ou no servidor.

### Médio Prazo (v1.2)
1. **Controle de Acesso por Permissão (Músico vs Operador):**
   * Limitar a visualização e controle do usuário logado baseado no seu papel (ex: um músico com papel `AUX1` só pode mexer nos envios do seu próprio auxiliar e não tem acesso ao Master nem ao EQ dos outros canais).
2. **Implementação de Gate de Ruído:**
   * Adicionar aba no modal de efeitos para o Noise Gate da 01V96 (Element `0x21`: Threshold, Range, Attack, Hold, Decay).
3. **Controle de Panorâmico (PAN) na Interface:**
   * Adicionar knobs ou controles deslizantes de PAN (Element `0x1B`) na faixa de canais.
4. **Gerenciador de Cenas (Snapshots):**
   * Possibilidade de salvar o estado de todos os faders e parâmetros no servidor em formato JSON e recarregá-los com um clique.

### Longo Prazo (v2.0)
1. **Medição Real de VU Meters:**
   * Decodificação dos pacotes SysEx periódicos de VU Meters da 01V96 para animação realística dos LEDs de pico e sinal na tela.
2. **Progressive Web App (PWA):**
   * Permitir instalação como app nativo na tela inicial do celular ou tablet dos músicos com funcionamento em tela cheia.
3. **Suporte Multi-Console:**
   * Abstração de drivers para permitir suporte futuro a outros consoles digitais da família Yamaha (01V96i, DM1000, 02R96, TF series).
