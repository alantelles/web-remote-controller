package main

import (
	"bytes"
	"testing"
)

func TestBuild01V96MeterRequestAndStop(t *testing.T) {
	// Deve gerar exatamente o pacote de 12 bytes observado no Studio Manager
	req := Build01V96MeterRequest(0x00, 0x05, 0x00, 16)
	expected := []byte{0xF0, 0x43, 0x30, 0x3E, 0x0D, 0x21, 0x00, 0x05, 0x00, 0x00, 0x10, 0xF7}
	if !bytes.Equal(req, expected) {
		t.Fatalf("MeterRequest mismatch: got % X, want % X", req, expected)
	}

	reqCh1 := Build01V96MeterRequest(0x00, 0x05, 0x02, 1)
	expectedCh1 := []byte{0xF0, 0x43, 0x30, 0x3E, 0x0D, 0x21, 0x00, 0x05, 0x02, 0x00, 0x01, 0xF7}
	if !bytes.Equal(reqCh1, expectedCh1) {
		t.Fatalf("MeterRequest CH3 mismatch: got % X, want % X", reqCh1, expectedCh1)
	}

	stop := Build01V96MeterStop()
	expectedStop := []byte{0xF0, 0x43, 0x30, 0x3E, 0x0D, 0x21, 0x7F, 0x00, 0x00, 0x00, 0x00, 0xF7}
	if !bytes.Equal(stop, expectedStop) {
		t.Fatalf("MeterStop mismatch: got % X, want % X", stop, expectedStop)
	}
}

func TestParse01V96MeterSysEx(t *testing.T) {
	// Resposta válida com 2 valores de 14 bits: (0x00, 0x10) -> 16 e (0x01, 0x02) -> 130
	pkt := []byte{0xF0, 0x43, 0x10, 0x3E, 0x0D, 0x21, 0x00, 0x05, 0x00, 0x00, 0x10, 0x01, 0x02, 0xF7}
	ul, lu, ll, vals, ok := Parse01V96MeterSysEx(pkt)
	if !ok {
		t.Fatal("esperava ok=true para pacote Remote Meter 0x21 válido")
	}
	if ul != 0x00 || lu != 0x05 || ll != 0x00 {
		t.Fatalf("endereço inesperado: %02X %02X %02X", ul, lu, ll)
	}
	if len(vals) != 2 || vals[0] != 16 || vals[1] != 130 {
		t.Fatalf("valores inesperados: %v", vals)
	}

	// Pacote 0x01 (Parameter Change comum) não deve ser aceito como Remote Meter
	paramPkt := []byte{0xF0, 0x43, 0x10, 0x3E, 0x7F, 0x01, 0x1C, 0x00, 0x00, 0x00, 0x00, 0x06, 0x37, 0xF7}
	if _, _, _, _, ok := Parse01V96MeterSysEx(paramPkt); ok {
		t.Fatal("não deveria aceitar Parameter Change 0x01 como Remote Meter 0x21")
	}

	// Pacote truncado (comprimento ímpar de payload) deve falhar
	badPkt := []byte{0xF0, 0x43, 0x10, 0x3E, 0x0D, 0x21, 0x00, 0x05, 0x00, 0x01, 0xF7}
	if _, _, _, _, ok := Parse01V96MeterSysEx(badPkt); ok {
		t.Fatal("não deveria aceitar pacote com payload ímpar")
	}
}
