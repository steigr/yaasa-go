package desk

import (
	"testing"
)

// TestDecodeHeightNotificationRejectsTwoBytePayload checks that a packet with
// a 2-byte payload (the uplift-ble format) is rejected.  The desk uses a
// 3-byte payload ([hi, lo, 0x00] whole mm); a 2-byte payload is invalid.
func TestDecodeHeightNotificationRejectsTwoBytePayload(t *testing.T) {
	// Packet: opcode=0x01, payloadLen=0x02, payload=[0x02, 0xD1], checksum, 0x7E
	// checksum = 0x01 + 0x02 + 0x02 + 0xD1 = 0xD6
	pkt := []byte{0xF2, 0xF2, 0x01, 0x02, 0x02, 0xD1, 0xD6, 0x7E}

	if h, ok := decodeHeightNotification(pkt); ok {
		t.Fatalf("decoded two-byte payload as %s, want rejection", h)
	}
}

// TestDecodeHeightNotificationAutomaticPushPayload checks a 3-byte payload
// packet with whole-mm encoding ([hi, lo, 0x00]).
// Ground truth: desk at 720 mm sends payload=[0x02, 0xD0, 0x00];
// 0x02D0 = 720.
func TestDecodeHeightNotificationAutomaticPushPayload(t *testing.T) {
	// Packet: opcode=0x01, payloadLen=0x03, payload=[0x02, 0xD0, 0x00]
	// checksum = 0x01 + 0x03 + 0x02 + 0xD0 + 0x00 = 0xD6
	pkt := []byte{0xF2, 0xF2, 0x01, 0x03, 0x02, 0xD0, 0x00, 0xD6, 0x7E}

	h, ok := decodeHeightNotification(pkt)
	if !ok {
		t.Fatal("expected height packet to decode")
	}
	if want := HeightFromMM(720); h != want {
		t.Fatalf("height = %s, want %s", h, want)
	}
}

// TestDecodeHeightNotificationRejectsNonHeightPacket checks that a packet
// with an opcode other than 0x01 is not decoded as a height value.
func TestDecodeHeightNotificationRejectsNonHeightPacket(t *testing.T) {
	// opcode=0x02 (move-down command echo), payloadLen=0x02
	// checksum = 0x02 + 0x02 + 0x1C + 0x20 = 0x40
	pkt := []byte{0xF2, 0xF2, 0x02, 0x02, 0x1C, 0x20, 0x40, 0x7E}

	if h, ok := decodeHeightNotification(pkt); ok {
		t.Fatalf("decoded non-height packet as %s", h)
	}
}
