# Análise de Performance — Tela do Console (`index.html` / `mobile.html`)

Foco: caminho "quente" (arrastar fader, receber eco/broadcast via WebSocket, render dos strips) e o backend que alimenta a tela.

## Resumo priorizado

| # | Severidade | Onde | Problema | Impacto |
|---|---|---|---|---|
| 1 | 🔴 Alta | [main.go#L307-L316](file:///home/alantelles/development/midi-test-go/main.go#L307-L316) | `broadcastWS` escreve **de forma síncrona** em todos os clientes sob `clientsLock`, sem `SetWriteDeadline` | Um celular com Wi‑Fi ruim trava os broadcasts de **todos** e também a goroutine de entrada MIDI → telas "congelam" / faders com atraso |
| 2 | 🔴 Alta | [main.go#L1763](file:///home/alantelles/development/midi-test-go/main.go#L1763) | `conn.WriteJSON(init)` **fora** do `clientsLock`, enquanto `broadcastWS` pode escrever na mesma conn | Gorilla não aceita escritas concorrentes → `panic: concurrent write to websocket connection` ocasional |
| 3 | 🟠 Média | [index.html#L6609-L6629](file:///home/alantelles/development/midi-test-go/index.html#L6609-L6629) (e master [L6188](file:///home/alantelles/development/midi-test-go/index.html#L6188-L6208), aux [L8182](file:///home/alantelles/development/midi-test-go/index.html#L8182-L8200)) | `getBoundingClientRect()` a cada `pointermove`, logo após escrever `cap.style.top` | **Layout forçado síncrono** (layout thrashing) a cada evento de movimento |
| 4 | 🟠 Média | [index.html#L5885](file:///home/alantelles/development/midi-test-go/index.html#L5885), CSS [L3229-L3238](file:///home/alantelles/development/midi-test-go/index.html#L3229-L3238) | Cap do fader posicionado via `top: %` + `transition: top` + `filter: drop-shadow` | `top` dispara **layout + repaint** do strip; `drop-shadow` é re-rasterizado a cada frame. Com 32 strips e broadcasts chegando, isso pesa em tablets/PCs fracos |
| 5 | 🟠 Média | [index.html#L5481](file:///home/alantelles/development/midi-test-go/index.html#L5481) / [L6625](file:///home/alantelles/development/midi-test-go/index.html#L6625) | DOM atualizado **imediatamente** em cada mensagem WS e em cada `pointermove` (sem `requestAnimationFrame`) | Várias escritas no mesmo canal por frame; em mouse de alta taxa (Firefox não alinha pointermove ao frame) gera trabalho desperdiçado e mais tráfego WS |
| 6 | 🟡 Baixa | [index.html#L5874-L5876](file:///home/alantelles/development/midi-test-go/index.html#L5874-L5876) e funções similares | 3× `getElementById` com template string por update | Custo pequeno, mas no caminho mais quente; fácil cachear |
| 7 | 🟡 Baixa | [main.go#L2028](file:///home/alantelles/development/midi-test-go/main.go#L2028) | Broadcast envia campo `hex` (`fmt.Sprintf("% X")`) em todo `param` + ecoa para o próprio remetente | Payload ~2× maior que o necessário; o cliente que arrasta recebe o próprio eco |
| 8 | 🟡 Baixa | CSS (≈55 ocorrências) | `transition: all ...` | Navegador monitora todas as propriedades; pode animar layout sem querer |
| 9 | 🟡 Baixa | [mobile.html#L3934-L3940](file:///home/alantelles/development/midi-test-go/mobile.html#L3934-L3940) | `input` do range: `getElementById` + `innerText` + `sendCommand` por evento | `innerText` força layout (use `textContent`); sem coalescência por frame |

> [!WARNING]
> **Bug funcional encontrado de passagem:** em [index.html#L9056](file:///home/alantelles/development/midi-test-go/index.html#L9050-L9058), `syncFullStateFromBackend` usa o seletor `.ch-strip[data-ch="i"] .ch-name`, mas os strips são `.channel-strip` com `#chBadge_i`. O nome do canal vindo da mesa **nunca é aplicado** na tela desktop.

---

## Soluções propostas

### 1 + 2. Backend: fila de escrita por cliente (maior ganho)
Trocar `map[*websocket.Conn]bool` por um struct cliente com canal de saída e **uma única goroutine escritora** por conexão:

```go
type wsClient struct {
    conn *websocket.Conn
    send chan []byte // buffer ex.: 256
}

func (c *wsClient) writePump() {
    for msg := range c.send {
        c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
        if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
            c.conn.Close(); return
        }
    }
}

func broadcastWS(msg WSMessage) {
    b, _ := json.Marshal(msg)        // serializa UMA vez, não N vezes
    clientsLock.Lock()
    for c := range clients {
        select {
        case c.send <- b:
        default: // cliente lento: descarta (ou desconecta) em vez de travar todos
        }
    }
    clientsLock.Unlock()
}
```
- O `init` (L1763) e o `gr_meter` ([midi_sync.go#L872](file:///home/alantelles/development/midi-test-go/midi_sync.go#L866-L878)) passam a usar `c.send` também → elimina a escrita concorrente.
- Bônus: `json.Marshal` uma vez por broadcast em vez de uma por cliente.

### 3. Cachear o retângulo do trilho no `pointerdown`
```js
let trackRect = null;
trackContainer.addEventListener('pointerdown', e => {
  trackRect = trackContainer.getBoundingClientRect(); // lê uma vez
  ...
});
function calcValueFromPointer(clientY) {
  const r = trackRect;
  const y = Math.max(0, Math.min(r.height, clientY - r.top));
  return (1 - y / r.height) * MAX_VALUE;
}
```
(Mesma mudança no master out e nos faders AUX do modal.)

### 4. Mover o cap com `transform` (compositor) em vez de `top`
```css
.fader-cap {
  top: 0;
  transform: translate(-50%, calc(var(--pos, 1) * var(--track-h) - 50%));
  will-change: transform;
  transition: transform 0.05s ease-out;
}
```
Alternativa mais simples: `cap.style.transform = \`translate(-50%, -50%) translateY(${px}px)\`` usando a altura do trilho cacheada (atualizada em `resize`/`ResizeObserver`).
- Substituir `filter: drop-shadow(...)` por `box-shadow` no `.fader-cap-body` (ou manter o filter, mas com `will-change: transform` a camada não é re-rasterizada ao mover).

### 5. Coalescer atualizações por frame
```js
const pendingFader = new Map(); let rafQueued = false;
function scheduleFaderUI(ch, val) {
  pendingFader.set(ch, val);
  if (!rafQueued) { rafQueued = true; requestAnimationFrame(flushUI); }
}
function flushUI() {
  rafQueued = false;
  for (const [ch, v] of pendingFader) updateFaderUI(ch, v);
  pendingFader.clear();
}
```
- No `pointermove`: atualizar UI via `scheduleFaderUI` e enviar `sendCommand` no máximo 1×/frame (ou a cada 16 ms) — o servidor já faz trailing‑edge, então nada se perde.
- No `handleWebSocketMessage`: usar `scheduleFaderUI` para `0x1C`, `0x23/level`, `0x4F`, `0x39`.

### 6. Cache de referências DOM
Após `createChannelStrip`, guardar `stripRefs[i] = { cap, db, raw, btnOn, btnPre, dest }` e usar nas funções `update*UI`. Evitar também reescrever `textContent`/`className` quando o valor não mudou.

### 7. Payload WS
- Omitir `hex` nos broadcasts `param` (enviar só quando houver painel de diagnóstico aberto) e usar `json:"hex,omitempty"`.
- Opcional: não ecoar para a conexão de origem enquanto ela arrasta (passar o `*wsClient` remetente para `broadcastWS` e pulá‑lo para mensagens não discretas).

### 8. CSS
Trocar `transition: all` por propriedades específicas (`background-color, border-color, color, box-shadow`).

### 9. Mobile
Cachear `dbText`/`slider` por canal, usar `textContent` em vez de `innerText`, e coalescer `sendCommand` por `requestAnimationFrame`.

---

## Ordem sugerida de implementação
1. Fila de escrita por cliente no backend (itens 1, 2, 7) — resolve travamentos e o risco de panic.
2. Cache do rect + coalescência por rAF (itens 3, 5).
3. `transform` no cap + cache de refs (itens 4, 6).
4. Ajustes de CSS e mobile (8, 9) + correção do bug do nome do canal.
