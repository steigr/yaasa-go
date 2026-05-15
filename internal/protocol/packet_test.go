package protocol

import (
	"bytes"
	"testing"
)

func TestMakeCommandMatchesUpliftBLEExamples(t *testing.T) {
	tests := []struct {
		name    string
		opcode  byte
		payload []byte
		want    []byte
	}{
		{
			name:   "wake",
			opcode: 0x00,
			want:   []byte{0xF1, 0xF1, 0x00, 0x00, 0x00, 0x7E},
		},
		{
			name:   "move up",
			opcode: 0x01,
			want:   []byte{0xF1, 0xF1, 0x01, 0x00, 0x01, 0x7E},
		},
		{
			name:    "move to 1372mm",
			opcode:  0x1B,
			payload: []byte{0x05, 0x5C},
			want:    []byte{0xF1, 0xF1, 0x1B, 0x02, 0x05, 0x5C, 0x7E, 0x7E},
		},
		{
			name:    "set units inches",
			opcode:  0x0E,
			payload: []byte{0x01},
			want:    []byte{0xF1, 0xF1, 0x0E, 0x01, 0x01, 0x10, 0x7E},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MakeCommand(tt.opcode, tt.payload...)
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("MakeCommand() = % X, want % X", got, tt.want)
			}
		})
	}
}

func TestDecodeResponsesValidatesChecksum(t *testing.T) {
	badChecksum := []byte{0xF2, 0xF2, 0x01, 0x03, 0x00, 0x1C, 0x20, 0x41, 0x7E}

	if packets := DecodeResponses(badChecksum); len(packets) != 0 {
		t.Fatalf("DecodeResponses() decoded bad checksum packet: %#v", packets)
	}
}

func TestDecodeResponsesParsesBackToBackPackets(t *testing.T) {
	currentHeight := []byte{0xF2, 0xF2, 0x01, 0x03, 0x00, 0x1C, 0x20, 0x40, 0x7E}
	unit := []byte{0xF2, 0xF2, 0x0E, 0x01, 0x01, 0x10, 0x7E}
	buf := append(append([]byte{}, currentHeight...), unit...)

	packets := DecodeResponses(buf)
	if len(packets) != 2 {
		t.Fatalf("DecodeResponses() returned %d packets, want 2", len(packets))
	}
	if packets[0].Opcode != 0x01 || !bytes.Equal(packets[0].Payload, []byte{0x00, 0x1C, 0x20}) {
		t.Fatalf("first packet = %#v", packets[0])
	}
	if packets[1].Opcode != 0x0E || !bytes.Equal(packets[1].Payload, []byte{0x01}) {
		t.Fatalf("second packet = %#v", packets[1])
	}
}
