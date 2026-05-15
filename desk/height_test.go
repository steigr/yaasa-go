package desk

import (
	"bytes"
	"testing"
)

func TestHeightNotificationRawBytesUseTenthsOfMillimetres(t *testing.T) {
	got := HeightFromMM(720).RawBytes()
	want := []byte{0x1C, 0x20}
	if !bytes.Equal(got, want) {
		t.Fatalf("RawBytes() = % X, want % X", got, want)
	}
}

func TestHeightCommandMillimetreBytesUseWholeMillimetres(t *testing.T) {
	got := HeightFromMM(1372).MillimetreBytes()
	want := []byte{0x05, 0x5C}
	if !bytes.Equal(got, want) {
		t.Fatalf("MillimetreBytes() = % X, want % X", got, want)
	}
}
