package desk

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Height represents a desk height stored internally in millimetres.
//
// FE62 response packets (opcode 0x01) report whole millimetres in a big-endian
// uint16 at payload[0:2].  Ground truth: a desk physically measured at 793 mm
// sends [03 19 00], and 0x0319 = 793 — confirming whole mm, not tenths-of-mm
// as some uplift-ble documentation suggests.
type Height float64

// HeightFromRaw converts a raw uint16 (tenths of millimetres, big-endian
// encoding used by uplift-ble tools) into a Height.
//
// This encoding is kept for compatibility with external uplift-ble tooling; it
// is NOT used for decoding FE62 opcode-0x01 height notifications in this
// package (those use whole mm).
func HeightFromRaw(raw uint16) Height {
	return Height(float64(raw) / 10.0)
}

// HeightFromRawBytes reads two big-endian bytes as a tenths-of-mm value.
//
// Same caveats as HeightFromRaw: retained for compatibility, not used
// internally for FE62 decoding.
func HeightFromRawBytes(b []byte) Height {
	return HeightFromRaw(binary.BigEndian.Uint16(b))
}

// HeightFromMM creates a Height from a millimetre value.
func HeightFromMM(mm float64) Height {
	return Height(mm)
}

// HeightFromInches creates a Height from an inch value.
func HeightFromInches(in float64) Height {
	return Height(in * 25.4)
}

// Raw returns the tenths-of-mm uint16 encoding (rounded).
//
// This is the uplift-ble wire format; not used internally for FE60 FE62
// decoding in this package.
func (h Height) Raw() uint16 {
	return uint16(math.Round(float64(h) * 10.0))
}

// RawBytes returns two big-endian bytes in tenths-of-mm encoding.
//
// Kept for compatibility with uplift-ble tools; not used internally for
// FE60 FE62 decoding.
func (h Height) RawBytes() []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, h.Raw())
	return b
}

// MillimetreBytes returns a two-byte big-endian whole-millimetre value.
//
// Used for opcode 0x1B (move-to-height command): the FE61 payload encodes the
// target height in whole millimetres, not tenths-of-mm.
func (h Height) MillimetreBytes() []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, uint16(math.Round(float64(h))))
	return b
}

// MM returns the height in millimetres.
func (h Height) MM() float64 {
	return float64(h)
}

// Inches returns the height in inches.
func (h Height) Inches() float64 {
	return float64(h) / 25.4
}

// String returns a human-readable millimetre string, e.g. "720.0 mm".
func (h Height) String() string {
	return fmt.Sprintf("%.1f mm", float64(h))
}

// InchesString returns a human-readable inch string, e.g. "28.3 in".
func (h Height) InchesString() string {
	return fmt.Sprintf("%.1f in", h.Inches())
}

// Near reports whether |h - other| <= tolerance.
//
// Used by WaitForHeight to decide when the desk has reached its target:
// a tolerance of 2 mm is typical (±1 motor step).
func (h Height) Near(other, tolerance Height) bool {
	diff := h - other
	if diff < 0 {
		diff = -diff
	}
	return diff <= tolerance
}
