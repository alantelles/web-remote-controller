# Especificação Técnica de Parâmetros SysEx — Yamaha 01V96 / 01V96V2

> **Documento de Referência:** Mapeamento do Protocolo MIDI System Exclusive (SysEx)  
> **Console:** Yamaha 01V96 / 01V96V2 / 01V96i / Studio Manager  
> **Status:** 100% Validado por Engenharia Reversa e Captura em Tempo Real no Studio Manager

---

## 1. Estrutura Fundamental do Pacote SysEx

Toda alteração de parâmetro em tempo real na Yamaha 01V96/01V96V2 utiliza a mensagem de **14 bytes** de *Parameter Change* no formato **Universal Digital Mixer**:

```
+----+----+----+----+----+----+----+----+----+-----+-----+-----+-----+----+
| F0 | 43 | 1n | 3E | 7F | 01 | ee | pp | cc | dd1 | dd2 | dd3 | dd4 | F7 |
+----+----+----+----+----+----+----+----+----+-----+-----+-----+-----+----+
   0    1    2    3    4    5    6    7    8    9    10    11    12   13
```

### Detalhamento Campo a Campo

| Byte | Valor Hex | Descrição |
|---|---|---|
| 0 | `0xF0` | **SOX (Start of Exclusive):** Início da mensagem SysEx. |
| 1 | `0x43` | **Yamaha ID:** Identificador de fabricante Yamaha. |
| 2 | `0x1n` / `0x3n` | **Sub-status / Canal MIDI:**<br>• `0x10`: Parameter Change (Escrita em tempo real).<br>• `0x30`: Parameter Request (Solicitação de leitura).<br>• `n`: Canal MIDI da mesa (Padrão: `0` para Canal 1). |
| 3 | `0x3E` | **Group ID:** Categoria *Digital Mixer*. |
| 4 | `0x7F` | **Model ID Universal:** Cabeçalho padrão do Yamaha Studio Manager / Digital Mixers (`0x7F`). |
| 5 | `0x01` | **Data Type:** `0x01` indica o *Edit Buffer* (área ativa de processamento). |
| 6 | `[ee]` | **Element No. (Elemento Funcional):** Identifica o bloco (ex: Fader, Aux Send, EQ, Compressor). |
| 7 | `[pp]` | **Parameter No. (Sub-parâmetro):** Propriedade dentro do bloco (ex: Nível, Pan, Q, Freq, Threshold). |
| 8 | `[cc]` | **Channel No. (Índice de Canal):** Zero-indexed (`0x00` a `0x1F` para CH 1..32; `0x00` a `0x07` para Aux 1..8). |
| 9 | `[dd1]` | **Data Byte 1:** Bits 27..21 (bit de sinal e mais significativos). |
| 10 | `[dd2]` | **Data Byte 2:** Bits 20..14. |
| 11 | `[dd3]` | **Data Byte 3:** Bits 13..7. |
| 12 | `[dd4]` | **Data Byte 4:** Bits 6..0 (bits menos significativos). |
| 13 | `0xF7` | **EOX (End of Exclusive):** Fim da mensagem SysEx. |

---

## 2. Codificação Matemática de 28 Bits com Sinal (Two's Complement)

A Yamaha representa os valores de controle através de **inteiros de 28 bits com sinal em complemento de 2**, divididos em 4 bytes de 7 bits (`dd1` a `dd4`):

### Montagem do Pacote (Go)
```go
u := uint32(value) & 0x0FFFFFFF
dd1 := byte((u >> 21) & 0x7F)
dd2 := byte((u >> 14) & 0x7F)
dd3 := byte((u >> 7) & 0x7F)
dd4 := byte(u & 0x7F)
```

### Decodificação do Pacote (Go)
```go
raw := (uint32(dd1) << 21) | (uint32(dd2) << 14) | (uint32(dd3) << 7) | uint32(dd4)
var value int
if (raw & 0x08000000) != 0 {
    // Número negativo (sinal ativo no bit 27)
    value = int(int32(raw | 0xF0000000))
} else {
    value = int(raw)
}
```

### Exemplos Práticos Reais de Valores

| Parâmetro | Valor Real | Bytes MIDI (`dd1 dd2 dd3 dd4`) | Explicação |
|---|---|---|---|
| Fader Unity (0.0 dB) | `823` | `00 00 06 37` | `823 = (6 << 7) | 55` |
| Fader Máximo (+10.0 dB) | `1023` | `00 00 07 7F` | Fader 100% |
| EQ Gain 0.0 dB | `0` | `00 00 00 00` | Centro Flat |
| EQ Gain +18.0 dB | `+180` | `00 00 01 34` | +18.0 dB em décimos de dB |
| EQ Gain -18.0 dB | `-180` | `7F 7F 7E 4C` | -18.0 dB em complemento de 2 |
| Comp Threshold 0.0 dB | `0` | `00 00 00 00` | Máximo |
| Comp Threshold -49.6 dB | `-496` | `7F 7F 7C 10` | Décimos de dB negativo |
| Pan Center | `0` | `00 00 00 00` | Centro |
| Pan L63 | `-63` | `7F 7F 7F 41` | Totalmente à esquerda |
| Pan R63 | `+63` | `00 00 00 3F` | Totalmente à direita |

---

## 3. Catálogo Completo de Elementos e Parâmetros (Validado)

### 3.1. Canais de Entrada (Input Channels 1 a 32)
* `[cc]` varia de `0x00` (Canal 1) a `0x1F` (Canal 32).

| Element (`ee`) | Param (`pp`) | Nome do Parâmetro | Faixa de Valores | Descrição Técnica |
|---|---|---|---|---|
| `0x1C` | `0x00` | **Input Fader Level** | `0` a `1023` | Nível do canal para o barramento Stereo (`823` = 0.0 dB). |
| `0x1A` | `0x00` | **Input Channel ON / MUTE** | `0` ou `1` | Chave geral do canal (`0` = Mute, `1` = ON). |
| `0x1B` | `0x00` | **Input Pan** | `-63` a `+63` | Panorâmica estéreo (`-63` = L63, `0` = Center, `+63` = R63). |
| `0x1D` | `0x00` | **Input Attenuator** | `-960` a `+120` | Ganho digital de entrada em décimos de dB. |

---

### 3.2. Envio para Auxiliares por Canal (Aux Sends 1 a 8)
* Elemento: **`0x23`**
* `[cc]` varia de `0x00` a `0x1F` (Canais 1 a 32).
* **Estrutura por passos de 3:** Para o auxiliar `auxIdx` (onde Aux 1 = 0, ..., Aux 8 = 7):
  * **ON / MUTE:** `pp = auxIdx * 3 + 0`
  * **PRE / POST:** `pp = auxIdx * 3 + 1` (`0` = Post-Fader, `1` = Pre-Fader)
  * **SEND LEVEL:** `pp = auxIdx * 3 + 2` (`0` a `1023`, `823` = 0.0 dB)

| Auxiliar | Send ON (`pp`) | PRE / POST (`pp`) | Send Level (`pp`) |
|---|---|---|---|
| **Aux 1** | `0x00` | `0x01` | `0x02` |
| **Aux 2** | `0x03` | `0x04` | `0x05` |
| **Aux 3** | `0x06` | `0x07` | `0x08` |
| **Aux 4** | `0x09` | `0x0A` | `0x0B` |
| **Aux 5** | `0x0C` | `0x0D` | `0x0E` |
| **Aux 6** | `0x0F` | `0x10` | `0x11` |
| **Aux 7** | `0x12` | `0x13` | `0x14` |
| **Aux 8** | `0x15` | `0x16` | `0x17` |

---

### 3.3. Equalizador Paramétrico de 4 Bandas e Atenuador
* Elemento de Entrada: **`0x20`** (Canais 1 a 32, `cc = 0x00..0x1F`)
* Elemento Master Stereo: **`0x50`** (`cc = 0x00`)
* Elemento Aux Masters: **`0x35`** (`cc = 0x00..0x07` para Aux 1..8)
* Elemento Atenuador Digital: **`0x1D`** (Canais 1 a 32, `pp = 0x00`, `cc = 0x00..0x1F`)

| Param (`pp`) | Parâmetro | Faixa de Valores | Descrição |
|---|---|---|---|
| `0x0F` | **EQ ON / Bypass** | `0` ou `1` | `0` = Bypass (Desligado), `1` = EQ Ativo (Ligado) |
| `0x01` | **LOW Q / Filter Type** | `0` a `41` | `0x00` = HPF (Passa-Altas), `0x29` (41) = L.SHELF, `0..40` (`40 - Q`) = Peaking (0.10 a 10.0) |
| `0x02` | **LOW Frequency** | `0` a `124` | 125 passos logarítmicos (20.0 Hz a 20.0 kHz, `0`=20.0 Hz, `124`=20.0 kHz) |
| `0x03` | **LOW Gain** | `-180` a `+180` | Ganho de -18.0 dB a +18.0 dB em décimos de dB (`0` = 0.0 dB). Chave ON/OFF quando em HPF |
| `0x05` | **LO-MID Q** | `0` a `40` | Fator Q de 0.10 a 10.0 (`40 - Q`) |
| `0x06` | **LO-MID Frequency**| `0` a `124` | 125 passos logarítmicos (20.0 Hz a 20.0 kHz) |
| `0x07` | **LO-MID Gain** | `-180` a `+180` | Ganho em décimos de dB |
| `0x08` | **HI-MID Q** | `0` a `40` | Fator Q de 0.10 a 10.0 (`40 - Q`) |
| `0x09` | **HI-MID Frequency**| `0` a `124` | 125 passos logarítmicos (20.0 Hz a 20.0 kHz) |
| `0x0A` | **HI-MID Gain** | `-180` a `+180` | Ganho em décimos de dB |
| `0x0B` | **HIGH Q / Filter Type**| `0` a `43` | `0x2B` (43) = LPF (Passa-Baixas), `0x2A` (42) = H.SHELF, `0..40` (`40 - Q`) = Peaking |
| `0x0C` | **HIGH Frequency** | `0` a `124` | 125 passos logarítmicos (20.0 Hz a 20.0 kHz) |
| `0x0D` | **HIGH Gain** | `-180` a `+180` | Ganho em décimos de dB. Chave ON/OFF quando em LPF |

#### Atenuador Digital de Entrada (Elemento `0x1D`)
* `ee = 0x1D`, `pp = 0x00`, `cc = 0x00..0x1F` (Canais 1 a 32)
* Faixa: `-960` a `+120` décimos de dB (`-96.0 dB` a `+12.0 dB`, nominal `0` = 0.0 dB).

---

### 3.4. Compressor de Canal
* Elemento de Entrada: **`0x1F`** (Canais 1 a 32, `cc = 0x00..0x1F`)
* Elemento Master Stereo: **`0x51`** (`cc = 0x00`)

| Param (`pp`) | Parâmetro | Faixa de Valores | Descrição |
|---|---|---|---|
| `0x01` | **COMP ON / Bypass** | `0` ou `1` | `0` = Bypass, `1` = Compressor Ativo |
| `0x09` | **Threshold** | `-540` a `0` | Limiar de -54.0 dB a 0.0 dB em décimos de dB negativos |
| `0x06` | **Ratio** | `0` a `15` | `0`=1.0:1 até `15`=∞:1 |
| `0x04` | **Attack** | `0` a `120` | Tempo de ataque de 0 ms a 120 ms |
| `0x05` | **Release** | `0` a `159` | Tempo de liberação em 160 passos (5 ms a 42.3 s) |
| `0x08` | **Knee** | `0` a `5` | Curvatura (`0` = Hard, `1` a `5` = Soft 1 a 5) |
| `0x07` | **Out Gain (Makeup)**| `0` a `180` | Ganho de saída de 0.0 dB a +18.0 dB em décimos de dB |

---

### 3.5. Saídas Master (Stereo Master e Aux Masters)

#### Stereo Master Out (L/R Geral)
* `[cc]` = `0x00`

| Element (`ee`) | Param (`pp`) | Descrição | Faixa de Valores |
|---|---|---|---|
| `0x4F` | `0x00` | **Stereo Out Fader** | `0` a `1023` (`823` = 0.0 dB) |
| `0x4D` | `0x00` | **Stereo Out ON / MUTE** | `0` = Mute, `1` = ON |
| `0x50` | Vários | **Stereo Master EQ** | Mapeamento idêntico ao EQ (`0x20`) |
| `0x51` | Vários | **Stereo Master COMP** | Mapeamento idêntico ao Comp (`0x1F`) |

#### Aux Masters 1 a 8
* `[cc]` varia de `0x00` (Aux 1) a `0x07` (Aux 8).

| Element (`ee`) | Param (`pp`) | Descrição | Faixa de Valores |
|---|---|---|---|
| `0x39` | `0x00` | **Aux Master Fader** | `0` a `1023` (`823` = 0.0 dB) |
| `0x36` | `0x00` | **Aux Master ON / MUTE** | `0` = Mute, `1` = ON |
| `0x35` | Vários | **Aux Master EQ** | Mapeamento idêntico ao EQ (`0x20`) |

---

## 4. Curva de Calibração do Fader (0 a 1023 <-> Decibéis dB)

A tabela oficial da Yamaha 01V96V2 define os seguintes pontos de referência com interpolação linear por trecho (*piecewise linear*):

| Decimal | dB |
|---|---|
| `0` | `-∞ dB` |
| `224` | `-40.0 dB` |
| `464` | `-20.0 dB` |
| `624` | `-10.0 dB` |
| `763` | `-5.0 dB` |
| **`823`** | **`0.0 dB` (Nominal / Unity Gain)** |
| `923` | `+5.0 dB` |
| `1023` | `+10.0 dB` |
