# Especificação Técnica de Parâmetros SysEx — Yamaha 01V96 / 01V96V2

> **Documento de Referência:** Mapeamento do Protocolo MIDI System Exclusive (SysEx)  
> **Console:** Yamaha 01V96 / 01V96V2 / 01V96i  
> **Documento Original de Origem:** *Yamaha 01V96V2 SysEx Parameter Address List*

---

## 1. Estrutura Fundamental do Pacote SysEx

Toda alteração de parâmetro em tempo real na Yamaha 01V96V2 utiliza a mensagem de **14 bytes** de *Parameter Change* no buffer de edição (*Edit Buffer*):

```
+----+----+----+----+----+----+----+----+----+-----+-----+-----+-----+----+
| F0 | 43 | 1n | 3E | 0D | 01 | ee | pp | cc | dd1 | dd2 | dd3 | dd4 | F7 |
+----+----+----+----+----+----+----+----+----+-----+-----+-----+-----+----+
   0    1    2    3    4    5    6    7    8    9    10    11    12   13
```

### Detalhamento Campo a Campo

| Byte | Valor Hex | Descrição |
|---|---|---|
| 0 | `0xF0` | **SOX (Start of Exclusive):** Início da mensagem SysEx. |
| 1 | `0x43` | **Yamaha ID:** Identificador de fabricante da Yamaha. |
| 2 | `0x1n` / `0x3n` | **Sub-status / Canal MIDI:**<br>• `1n` = Parameter Change (Escrita em tempo real).<br>• `3n` = Parameter Request (Solicitação de leitura).<br>• `n` = MIDI Device ID / Canal (de `0` a `F`, correspondente ao canal MIDI 1 a 16. Padrão: `0x10` para Canal 1). |
| 3 | `0x3E` | **Group ID:** Categoria *Digital Mixer*. |
| 4 | `0x0D` | **Model ID:** Identificador exclusivo da **01V96**. |
| 5 | `0x01` | **Data Type:** `0x01` indica o *Edit Buffer* (área de memória ativa da mesa). |
| 6 | `[ee]` | **Element No. (Elemento Funcional):** Identifica o bloco (ex: Fader, EQ, Compressor, Aux Send). |
| 7 | `[pp]` | **Parameter No. (Sub-parâmetro):** Identifica a propriedade dentro do bloco (ex: Ganho, Frequência, Q, Threshold). |
| 8 | `[cc]` | **Channel No. (Índice de Canal):** Endereço zero-indexed do canal ou saída (`0x00` a `0x1F` para canais de entrada 1 a 32; `0x00` a `0x07` para Aux 1 a 8). |
| 9 | `[dd1]` | **Data Byte 1:** Byte mais significativo do valor (normalmente `0x00`). |
| 10 | `[dd2]` | **Data Byte 2:** Byte intermediário (normalmente `0x00`). |
| 11 | `[dd3]` | **Data Byte 3:** 7 bits mais significativos de dados `(value >> 7) & 0x7F`. |
| 12 | `[dd4]` | **Data Byte 4:** 7 bits menos significativos de dados `value & 0x7F`. |
| 13 | `0xF7` | **EOX (End of Exclusive):** Fim da mensagem SysEx. |

---

## 2. Codificação e Empacotamento de Valores de Dados

No protocolo MIDI, os bytes de dados não podem ter o oitavo bit (`MSB`) ativo (valores válidos são de `0x00` a `0x7F`). Por isso, a Yamaha distribui o número inteiro de 14 bits entre `dd3` e `dd4`:

```go
dd1 := byte(0x00)
dd2 := byte(0x00)
dd3 := byte((value >> 7) & 0x7F)
dd4 := byte(value & 0x7F)
```

Para decodificar um valor recebido nos 4 bytes:
```go
value := (int(dd1) << 21) | (int(dd2) << 14) | (int(dd3) << 7) | int(dd4)
```

---

## 3. Catálogo Completo de Elementos e Parâmetros Implementados

### 3.1. Canais de Entrada (Input Channels 1 a 32)
* `[cc]` varia de `0x00` (Canal 1) a `0x1F` (Canal 32).

| Element (`ee`) | Param (`pp`) | Nome do Parâmetro | Faixa de Valores | Descrição Técnica |
|---|---|---|---|---|
| `0x1C` | `0x00` | **Input Fader Level** | `0` a `1023` | Nível do fader para o barramento Stereo. `0` = -∞ dB, `823` = 0.0 dB, `1023` = +10.0 dB. |
| `0x1A` | `0x00` | **Input Channel ON / MUTE** | `0` ou `1` | Chave liga/desliga do canal. `0` = Muted (Apagado), `1` = ON (Ativo). |
| `0x1B` | `0x00` | **Input Pan** | `0` a `126` | Posição panorâmica. `0` = L63, `63` = Center, `126` = R63. |
| `0x23` | `0x00` a `0x07` | **Aux Send Level (1 a 8)** | `0` a `1023` | Nível de envio individual para o Auxiliar correspondente (`pp=0x00` é Aux 1, `pp=0x07` é Aux 8). |
| `0x23` | `0x08` a `0x0F` | **Aux Send ON / MUTE (1 a 8)** | `0` ou `1` | Liga/Desliga do envio para o Auxiliar (`pp=0x08` é Aux 1 ON, `pp=0x0F` é Aux 8 ON). |
| `0x23` | `0x10` a `0x17` | **Aux Send Pre/Post (1 a 8)** | `0` ou `1` | Ponto de roteamento: `0` = Post-Fader, `1` = Pre-Fader. |

---

### 3.2. Equalizador Paramétrico de Entrada (Input EQ)
* Elemento: `0x20`
* `[cc]` varia de `0x00` a `0x1F` (Canais 1 a 32).

| Param (`pp`) | Parâmetro | Faixa de Valores | Mapeamento e Unidades |
|---|---|---|---|
| `0x00` | **EQ ON / Bypass** | `0` ou `1` | `0` = Bypass / Desligado, `1` = EQ Ligado |
| `0x01` | **LOW Q / Filter Type** | `0` a `40` | `0` = Shelving / HPF, valores `1` a `40` = Q de 10.0 a 0.10 |
| `0x02` | **LOW Frequency** | `0` a `138` | Frequência de 20 Hz a 20 kHz (escala logarítmica) |
| `0x03` | **LOW Gain** | `0` a `72` | Ganho de -18.0 dB (`0`) a +18.0 dB (`72`). `36` = 0.0 dB |
| `0x04` | **LO-MID Q** | `0` a `40` | Fator de mérito Q: de 10.0 a 0.10 |
| `0x05` | **LO-MID Frequency** | `0` a `138` | Frequência de 20 Hz a 20 kHz |
| `0x06` | **LO-MID Gain** | `0` a `72` | Ganho de -18.0 dB (`0`) a +18.0 dB (`72`). `36` = 0.0 dB |
| `0x07` | **HI-MID Q** | `0` a `40` | Fator de mérito Q: de 10.0 a 0.10 |
| `0x08` | **HI-MID Frequency** | `0` a `138` | Frequência de 20 Hz a 20 kHz |
| `0x09` | **HI-MID Gain** | `0` a `72` | Ganho de -18.0 dB (`0`) a +18.0 dB (`72`). `36` = 0.0 dB |
| `0x0A` | **HIGH Q / Filter Type** | `0` a `40` | `0` = Shelving / LPF, valores `1` a `40` = Q de 10.0 a 0.10 |
| `0x0B` | **HIGH Frequency** | `0` a `138` | Frequência de 20 Hz a 20 kHz |
| `0x0C` | **HIGH Gain** | `0` a `72` | Ganho de -18.0 dB (`0`) a +18.0 dB (`72`). `36` = 0.0 dB |
| `0x0D` | **HPF ON / OFF** | `0` ou `1` | `0` = HPF Desligado, `1` = HPF Ativo na banda LOW |
| `0x0E` | **LPF ON / OFF** | `0` ou `1` | `0` = LPF Desligado, `1` = LPF Ativo na banda HIGH |

---

### 3.3. Compressor de Entrada (Input Compressor)
* Elemento: `0x22`
* `[cc]` varia de `0x00` a `0x1F` (Canais 1 a 32).

| Param (`pp`) | Parâmetro | Faixa de Valores | Mapeamento e Unidades |
|---|---|---|---|
| `0x00` | **COMP ON / Bypass** | `0` ou `1` | `0` = Bypass, `1` = Compressor Ativo |
| `0x01` | **Threshold** | `0` a `54` | Limiar: `0` = -54 dB até `54` = 0 dB (passo de 1 dB) |
| `0x02` | **Ratio** | `0` a `15` | `0`=1.0:1, `1`=1.1:1, `2`=1.3:1, `3`=1.5:1, `4`=1.7:1, `5`=2.0:1, `6`=2.5:1, `7`=3.0:1, `8`=3.5:1, `9`=4.0:1, `10`=5.0:1, `11`=6.0:1, `12`=8.0:1, `13`=10:1, `14`=20:1, `15`=∞:1 |
| `0x03` | **Attack** | `0` a `120` | Tempo de ataque: de 0 ms a 120 ms |
| `0x04` | **Release** | `0` a `79` | Tempo de liberação: de 5 ms a 42.3 s |
| `0x05` | **Knee** | `0` a `5` | Curvatura do joelho: `0` = Hard Knee, `1` a `5` = Soft Knee 1 a 5 |
| `0x06` | **Output Gain (Makeup)**| `0` a `36` | Ganho de compensação de saída: `0` = 0.0 dB até `36` = +18.0 dB (passo de 0.5 dB) |

---

### 3.4. Auxiliares Masters (Aux Masters 1 a 8)
* `[cc]` varia de `0x00` (Aux 1) a `0x07` (Aux 8).

| Element (`ee`) | Param (`pp`) | Nome do Parâmetro | Faixa de Valores | Descrição Técnica |
|---|---|---|---|---|
| `0x31` | `0x00` | **Aux Master Fader** | `0` a `1023` | Nível master da saída auxiliar (`823` = 0.0 dB) |
| `0x2F` | `0x00` | **Aux Master ON / MUTE**| `0` ou `1` | Chave geral da saída auxiliar (`0` = Mute, `1` = ON) |
| `0x35` | `0x00` a `0x0E`| **Aux Master Equalizer**| *Idem 0x20* | Equalizador de 4 bandas do Master Auxiliar |
| `0x36` | `0x00` a `0x06`| **Aux Master Compressor**| *Idem 0x22* | Compressor do Master Auxiliar |

---

### 3.5. Saída Master Stereo (Stereo Master Out)
* `[cc]` = `0x00`

| Element (`ee`) | Param (`pp`) | Nome do Parâmetro | Faixa de Valores | Descrição Técnica |
|---|---|---|---|---|
| `0x4D` | `0x00` | **Stereo Out Fader** | `0` a `1023` | Fader Geral da mesa (Master L/R). `823` = 0.0 dB |
| `0x4B` | `0x00` | **Stereo Out ON / MUTE**| `0` ou `1` | Mute geral do Master Stereo (`0` = Mute, `1` = ON) |
| `0x50` | `0x00` a `0x0E`| **Stereo Master EQ** | *Idem 0x20* | Equalizador de 4 bandas do Stereo Out |
| `0x51` | `0x00` a `0x06`| **Stereo Master COMP** | *Idem 0x22* | Compressor do Stereo Out |

---

### 3.6. Demais Elementos Mapeados da 01V96 (Para Expansão Futura)

| Element (`ee`) | Bloco Funcional | Sub-elementos / Params |
|---|---|---|
| `0x21` | **Input Noise Gate** | `pp=0x00` (On), `0x01` (Threshold), `0x02` (Range), `0x03` (Attack), `0x04` (Hold), `0x05` (Decay) |
| `0x2B` | **Bus Master ON / MUTE** | Canais `0x00` a `0x07` (Bus 1 a 8) |
| `0x2D` | **Bus Master Fader** | Canais `0x00` a `0x07` (Bus 1 a 8, valor 0 a 1023) |
| `0x33` | **Bus Master EQ** | Equalizador de 4 bandas dos barramentos Bus 1 a 8 |
| `0x34` | **Bus Master Compressor**| Compressor dos barramentos Bus 1 a 8 |
| `0x1D` | **Effects Return ON / MUTE** | Retornos de Efeitos internos 1 a 4 |
| `0x1F` | **Effects Return Fader** | Faders de Retorno de Efeitos 1 a 4 |

---

## 4. Curvas de Conversão e Calibração

### 4.1. Conversão de Fader (Decimal 0 a 1023 <-> Decibéis dB)
A tabela oficial da Yamaha 01V96V2 define os seguintes marcos de calibração por interpolação linear por trecho (*piecewise linear*):

| Valor Decimal | Nível em dB Exibido |
|---|---|
| `0` | `-∞ dB` |
| `224` | `-40.0 dB` |
| `464` | `-20.0 dB` |
| `624` | `-10.0 dB` |
| `763` | `-5.0 dB` |
| **`823`** | **`0.0 dB` (Nominal / Unity Gain)** |
| `923` | `+5.0 dB` |
| `1023` | `+10.0 dB` |

### 4.2. Escala de Frequências de Equalização (139 Passos)
A mesa distribui as frequências de corte de 20 Hz a 20 kHz em 139 índices (`0` a `138`).
Fórmula matemática aproximada para exibição em Hz:
$$\text{Freq}(idx) = 20 \times 10^{\frac{idx}{138} \times 3}$$

Exemplos de correspondência:
* `idx = 0` $\rightarrow$ 20 Hz
* `idx = 46` $\rightarrow$ 100 Hz
* `idx = 92` $\rightarrow$ 1.0 kHz
* `idx = 115` $\rightarrow$ 5.0 kHz
* `idx = 138` $\rightarrow$ 20.0 kHz

---

## 5. Implementação no Código-Fonte

### Montagem do Pacote SysEx em Go (`main.go`)
```go
func Build01V96SysEx(element, param, channel byte, value int) []byte {
    if value < 0 { value = 0 }
    if value > 1023 { value = 1023 }

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
```

### Transmissão no Frontend (`index.html`)
```javascript
function sendParam(element, param, channel, value) {
    if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({
            element: element,
            param: param,
            channel: channel,
            value: value
        }));
    }
}
```
