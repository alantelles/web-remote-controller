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
* **Seleção Dinâmica de Canais Visíveis & Visualização Salva:** Filtro por blocos (Canais 1-8, 9-16, 17-24, 25-32 ou customizados) e botão `⭐ Salvo` dedicado em ambas as interfaces (Desktop e Mobile). Inversão ergonômica de blocos (painel de filtros de canais no topo e seletor de Mix/Auxiliares imediatamente acima dos faders). Persistência de visualização customizada unificada via `localStorage` com proteção contra sobreposição acidental por bancos padrão.

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

### Etapa 7: Sistema de Autenticação e Perfis de Usuário (RBAC)
* **Segurança Multi-usuário:** Autenticação baseada em sessões seguras criptográficas via cookie `remote_session` (validade de 24 horas, proteção HttpOnly).
* **Gerenciamento Desacoplado via CSV:** Arquivo `usuarios.csv` contendo `nome,usuario,senha,papel,mix,canais,saidas`. O arquivo é relido em tempo de execução sem necessitar reiniciar o servidor.
* **Controle de Acesso Baseado em Papéis (RBAC de Tela e Backend):**
  * `ADMIN`: Acesso total irrestrito (FOH completo, sincronização geral com a mesa física, painel administrativo, salvamento/recall de cenas, snapshots, auto-save, gestão de operadores/limites e desligamento do sistema).
  * `OPERATOR`: Opera toda a superfície de controle física e virtual (faders, pans, equalizadores 4 bandas, compressores, gates, sends on fader), mas não acessa a seção administrativa nem dispara sincronização geral.
  * `BASIC`: Permissão estrita apenas para ajuste de **Volumes e Mute** (faders e botões ON/MUTE de canais, masters e envios aux). Controles de EQ, Dinâmica (Comp/Gate), Pan e Admin desabilitados na interface e estritamente filtrados no backend (WebSocket).
    * **Delimitação Granular de Entradas e Saídas:** O administrador pode delimitar exatamente quais canais de entrada (1 a 32) e saídas (Master Stereo e Auxiliares 1 a 8) o operador Básico pode acessar.
    * **Botão "Selecionar Todos":** Presente na interface administrativa para rápida concessão total de canais/saídas e na toolbar do console do operador (onde "Todos" seleciona todos os canais delimitados para aquele usuário).
    * **Aba Administrativa "👥 Operadores & Limites":** Gestão de operadores com badges visuais de limites e editor interativo em grade com ações rápidas (`CH 1–16`, `CH 17–32`, `Apenas Auxiliares`, `Selecionar Todos`). Endpoint dedicado `GET/POST /api/users`.
  * `PERFORMER`: Direcionado exclusivamente à futura tela de mixagem pessoal. Redirecionado automaticamente no login e nas rotas principais para `/mix?id=<mix_id>`, servindo a página dedicada `mix.html` ("It works!").
* **Telas Dedicadas:** Página de login profissional (`login.html`), rota `/logout`, endpoint de perfil `/api/me`, console geral (`index.html`), console mobile (`mobile.html`) e área de mix pessoal (`mix.html`).
* **Suíte de Testes Automatizados:** Testes unitários completos cobrindo autenticação, leitura de CSV com mix_id, RBAC por papel, bloqueios de endpoints administrativos e validação de comandos em `auth_test.go`.

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
* **Rack de Processamento & Equalizador Paramétrico (Modal SEL):**
  * Acionamento ao tocar no botão `SEL` de qualquer canal de entrada ou do fader Master da saída atual.
  * **Navegação de Canais (Prev / Next):** Botões táteis `◀` e `▶` no cabeçalho do modal (e setas do teclado `←`/`→`) para alternar instantaneamente entre os canais (1 a 32, Stereo Master e Auxiliares 1 a 8). Otimizado para não causar reflows/scroll no mixer de fundo enquanto o modal estiver aberto, sincronizando e posicionando o fader suavemente apenas no momento em que o modal for fechado.
  * **Abas dos Módulos do Rack:** Seletor em abas para `EQUALIZADOR`, `COMPRESSOR` e `GATE` (com LEDs indicadores de ativação verde). A aba Gate é ocultada automaticamente para saídas Master e Auxiliares.
  * **Ponte para o Console Completo (PC):** Botão `🖥️ PC` no cabeçalho e atalhos dedicados nos painéis de Compressor e Gate que abrem diretamente a versão desktop com o canal e a aba pré-selecionados (`/?view=desktop&fx=CH&tab=...`).
  * **Controle Rápido de Compressor no Mobile:** Botão `COMP: ON / BYPASS` funcional em tempo real com SysEx (`0x22`, `0x51`, `0x36`).
  * **Gráfico de Resposta em Frequência (Canvas):** Ocupa toda a largura da tela móvel (~125px de altura) com grade de frequências (100Hz, 1kHz, 10kHz), linhas de ganho (0 dB, ±12 dB), curva com gradiente e 4 marcadores coloridos com anel de foco na banda ativa.
  * **Seletor de Bandas:** 4 botões compactos em linha (`LOW`, `LO-MID`, `HI-MID`, `HIGH`) exibindo mini badges dinâmicos de frequência/ganho/filtro.
  * **Container Único de Edição:** Sliders ergonômicos de Frequência, Ganho (com botão `0 dB`) e Q (`touch-action: pan-y`).
  * **Filtros Integrados:** Sem botões avulsos de HPF/LPF — o filtro corte passa-altas (HPF) é selecionado diretamente no dropdown da banda `LOW`, e o passa-baixas (LPF) na banda `HIGH`, desabilitando e esmaecendo automaticamente Ganho e Q.
  * **Ações de Cabeçalho:** Alternador `EQ: ON` / `BYPASS` e botão `⚡ FLAT` para zerar as 4 bandas instantaneamente.

### Etapa 10: Mixagem Pessoal para Performers (Joystick 3 Eixos Equal Power & Admin)
* **Gerenciamento na Seção Admin (`🎧 Mixes Pessoais` no Desktop):**
  * Criação, edição, ativação/desativação e exclusão de Mixes Pessoais persistidas em `data/personal_mixes.json`.
  * Definição do canal de saída (`AUX 1` a `AUX 8`) e vínculo exclusivo de **1 Mix por usuário `PERFORMER`** (sincronizado automaticamente com `usuarios.csv` e sessões ativas).
  * Matriz visual interativa de 32 canais para distribuir os canais nos **3 grupos contextuais exclusivos**: **👤 Eu**, **🎤 Vozes** e **🎸 Outros** (com atalho `⚡ Livres → Outros`).
  * Captura e atualização automática dos **Volumes Base de Referência (`BaseLevels`)** configurados pelo Admin no respectivo Auxiliar.
* **Tela do Performer (`/mix` — Disco/Joystick de 3 Eixos):**
  * Controle circular interativo em Canvas (`touch-action: none`) com 3 pólos defasados em $120^\circ$ (**EU**, **VOZES**, **OUTROS**).
  * **Cálculo Equal Power ($w_{\text{eu}}^2 + w_{\text{vozes}}^2 + w_{\text{outros}}^2 = 1$):** Atua como multiplicador relativo sobre os `BaseLevels` ajustados pelo Admin naquele Auxiliar, emitindo os novos níveis de envio (`0x23`) em tempo real via WebSocket e barramento MIDI com throttling de 20 ms.

### Etapa 11: Mute Groups da Aplicação (Multi-Grupo com Proteção de Interseção)
* **Gerenciamento Independente na Aplicação (`mute_groups.go` & `data/mute_groups.json`):**
  * 4 Mute Groups fixos (`MUTE 1` a `MUTE 4`) definidos e persistidos pela própria aplicação, atuando diretamente sobre o parâmetro `ON/MUTE` (`0x1A`) dos canais de entrada (`1` a `32`) sem depender dos mute groups internos do hardware.
* **Lógica de Interseção de Grupos (Multi-Grupo Seguro):**
  * Um canal pode pertencer a múltiplos Mute Groups simultaneamente.
  * **Ativar Mute Group:** Muta imediatamente (`On = 0`) todos os canais cadastrados no grupo.
  * **Desativar Mute Group:** Só desmuta (`On = 1`) os canais do grupo que **não** pertençam a nenhum outro Mute Group que continue ativo (mutado).
  * **Override Manual Permitido:** O operador pode desmutar individualmente um canal na faixa do console mesmo com o Mute Group ativo.
* **Permissões (RBAC) e Interface (Desktop e Mobile):**
  * **Acionamento (Mutar/Desmutar Grupo):** Disponível direto na barra principal do console (Desktop e Mobile) para `ADMIN`, `OPERATOR` e `BASIC`.
  * **Configuração (Nome e Seleção de Canais 1–32):** Exclusiva para `ADMIN` através do botão `⚙️` ao lado dos Mute Groups na barra principal (ou clique direito no grupo no Desktop).
  * **Badges nos Canais (`M1`..`M4`):** Cada faixa de canal exibe quais Mute Groups integra, acendendo em vermelho quando o respectivo grupo está mutado.

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
3. **Equalizador 4 Bandas Paramétrico:** Ajustes finos de frequências, ganhos, Q e filtros com visualizador gráfico Canvas tanto no Desktop quanto no Mobile (`/m`).
4. **Compressor Dinâmico Completo:** Ajuste completo de dinâmica (Threshold, Ratio, Attack, Release, Knee, Make-up Gain) com curva de transferência dinâmica e presets (Padrão, Vocal, Bateria, Baixo, Limiter) no Desktop e Mobile.
5. **Noise Gate / Expander Dinâmico:** Controle completo de ruído (Threshold, Range com -∞ dB, Attack, Hold, Decay) com curva de transferência gráfica e presets (Padrão, Bumbo, Caixa, Tons, Vocal) no Desktop e Mobile (Canais 1-32, Element `0x21`).
6. **Rack Mobile Completo com Navegação de Canais:** Modal responsivo de tela cheia no mobile com navegação rápida entre canais (`◀` / `▶` e setas de teclado) com otimização de renderização que sincroniza a rolagem do mixer de fundo apenas ao fechar o diálogo.
7. **Autenticação:** Proteção de acesso com cookies de sessão e usuários configuráveis via CSV. Redirecionamento automático para `/m` ao autenticar em dispositivos móveis.
8. **Feedback Bidirecional em Tempo Real (MIDI IN -> WebSocket):** Escuta ativa na porta MIDI IN ALSA recebendo alterações de faders, mutes, envios, EQ e compressores realizadas na mesa física e propagando instantaneamente via broadcast WebSocket para todos os navegadores e dispositivos móveis conectados.
9. **Sincronização de Estado Sob Demanda (Lazy Parameter Requests):** Estratégia otimizada que reduz drasticamente o tráfego MIDI e saturação de barramento:
   * **Visão Principal (Boot / FOH):** Solicita apenas os parâmetros essenciais em tempo real (Stereo Master, Aux Masters 1 a 8, e os faders/mutes dos 32 canais de entrada).
   * **Ao alternar para Auxiliar (Sends on Fader):** Solicita sob demanda apenas os 32 envios (faders e mutes) daquele auxiliar específico.
   * **Ao abrir Rack de FX (EQ, Comp, Gate):** Solicita sob demanda exclusivamente os parâmetros de processamento dinâmico e equalização do canal ou saída selecionada.
   * **Sincronização Global sob demanda:** Disponível sob ação direta do usuário ("🔄 Puxar da Mesa") no Desktop e Mobile.
10. **Calibração Fiel da Escala de Faders de Saída (dBFS):** Alinhamento perfeito com a serigrafia física da 01V96V2 nas saídas (Stereo Master e Aux Masters 1 a 8):
   * Escala de saída em **dBFS** de **-∞ a 0 dBFS** (1023 no topo = 0.0 dB, 0 na base = -∞ dB), sem boost de +10 dB (inexistente nas saídas do hardware).
   * Valores iniciais e canais em repouso padronizados em 0 (-∞ dB), eliminando o salto indevido para 0 dB (823) em canais mínimos.
11. **Proteção contra Loopback e Eco Local:** Filtro de supressão de reflexão que descarta mensagens emitidas pela própria aplicação que retornam via portas ALSA como `Midi Through` ou `VirMIDI`.
12. **Ambiente de Desenvolvimento Sem Mesa:** Simulador multi-fader e logs detalhados de decodificação SysEx no console.
13. **Estabilidade de Tráfego:** Throttling ativo protegendo a saída MIDI contra estouro de buffer, com bypass automático para eventos discretos (Mute, On, Flat, Bypass de EQ, Comp e Gate).
14. **Navegação de Canais por Teclado no Desktop (Modo Shift 2x) & Seleção Múltipla:**
    * Pressionar a tecla `Shift` duas vezes consecutivas ativa o modo de seleção tátil no console web.
    * O canal em foco recebe uma aura luminosa (idêntica ao seletor mobile) e seu cabeçalho é destacado.
    * As setas `←` / `→` alternam o canal selecionado, realizando rolagem horizontal suave da viewport sempre que o canal estiver fora da área visível.
    * **Seleção de Canais Contíguos (Bloco):** ao segurar `Shift` e navegar com `←` / `→` (ou `↑` / `↓` no Mobile), seleciona uma faixa contígua de canais com a aura luminosa.
    * **Ajuste de Volume em Bloco:** `↑` / `↓` no PC (ou `←` / `→` no Mobile) altera o volume de **todos os canais selecionados simultaneamente**, preservando o equilíbrio relativo entre eles.
    * `Ctrl+↑`: no Desktop, ajusta instantaneamente o canal focado (ou bloco) para `0.0 dB` (823 nos canais ou 1023 no Master out).
    * `Ctrl+↓`: no Desktop, ajusta instantaneamente o canal focado (ou bloco) para `-∞ dB` (0 / mute de fader).
    * No Mobile (`/m`), como os faders são horizontais, os atalhos seguem o eixo do slider: `Ctrl+→` ajusta para `0.0 dB` e `Ctrl+←` para `-∞ dB`.
    * Teclas `0` e `1` a `8` continuam funcionais para comutação rápida de saída, e pressionar `Shift 2x` novamente (ou `Escape`) encerra o modo.
15. **Biblioteca de Presets de Canais (Channel Presets - Rack Completo):**
    * **Persistência em Software:** Presets são salvos exclusivamente como arquivos JSON no serviço (`data/channel_presets/<id>.json`), sem gravar ou ocupar memórias internas na mesa física.
    * **Processamento de Rack Completo:** Salva e carrega todas as informações do rack de canal (Equalizador 4 bandas, Atenuador digital, Compressor e Noise Gate).
    * **Isolamento da Mixagem:** Não guarda volumes (faders), estados de mute, pan, configurações de grupos nem envios auxiliares (nível, mute, pre/post), preservando intacta a mixagem de FOH e palco ao carregar um preset.
    * **Controle de Acesso RBAC (Operator+):** Apenas operadores e administradores (`OPERATOR` e `ADMIN`) podem salvar, carregar ou excluir presets. Usuários `BASIC` e `PERFORMER` possuem permissão restrita (403 Forbidden).
    * **Transmissão Cadenciada:** Ao carregar um preset, o serviço atualiza o Edit Buffer da mesa física transmitindo os comandos SysEx de forma cadenciada (~6ms) e sincroniza instantaneamente todas as interfaces Desktop e Mobile via WebSocket.

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
| **Persistência em Software vs. Edit Buffer da Mesa** | Gravar cenas/presets em User Libraries internas da mesa via SysEx Bulk Dump | Todos os controles que não necessitem de manipular o Edit Buffer são implementados em nível de software no próprio serviço (JSON em disco). A mesa física recebe apenas os parâmetros ativos no Edit Buffer (`0x01`) ao carregar/aplicar, evitando desgaste de memória flash, saturação de barramento e limitações de slots internos do console. |

---

## 5. Planos de Futuro e Roadmap Prioritário

### Curto Prazo (v1.1)
1. **Nomes Personalizados de Canais:**
   * Permitir que o usuário nomeie os canais na interface (ex: "Voz Principal", "Violão", "Bumbo") e salve localmente ou no servidor.
2. **Controle de Panorâmico (PAN) na Interface:**
   * Adicionar knobs ou controles deslizantes de PAN (Element `0x1B`) na faixa de canais.

### Médio Prazo (v1.2)
1. **Controle de Acesso por Permissão (Músico vs Operador):**
   * Limitar a visualização e controle do usuário logado baseado no seu papel (ex: um músico com papel `AUX1` só pode mexer nos envios do seu próprio auxiliar e não tem acesso ao Master nem ao EQ dos outros canais).
2. **Gerenciador de Cenas (Snapshots):**
   * Possibilidade de salvar o estado de todos os faders e parâmetros no servidor em formato JSON e recarregá-los com um clique.

### Longo Prazo (v2.0)
1. **Medição Real de VU Meters:**
   * Decodificação dos pacotes SysEx periódicos de VU Meters da 01V96 para animação realística dos LEDs de pico e sinal na tela.
2. **Progressive Web App (PWA):**
   * Permitir instalação como app nativo na tela inicial do celular ou tablet dos músicos com funcionamento em tela cheia.
3. **Suporte Multi-Console:**
   * Abstração de drivers para permitir suporte futuro a outros consoles digitais da família Yamaha (01V96i, DM1000, 02R96, TF series).
