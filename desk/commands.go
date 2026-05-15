package desk

import (
	"fmt"
	"time"

	"github.com/steigr/yaasa-go/internal/protocol"
)

// Opcode constants for the FE60 protocol.
// Core command sources: librick/uplift-ble (verified on FE60 variant),
// tzermias/deskctl. Save-preset opcodes are Yaasa/Jiecang-specific and are
// not documented by uplift-ble.
const (
	opWake                = 0x00
	opMoveUp              = 0x01
	opMoveDown            = 0x02
	opSavePreset1         = 0x03
	opSavePreset2         = 0x04
	opPreset1             = 0x05
	opPreset2             = 0x06
	opRequestHeightLimits = 0x07
	opMoveTo              = 0x1B
	opStop                = 0x2B
	opRequestSitStandTime = 0xA2 // Yaasa/Jiecang-specific; response is opcode 0xA2 on FE62
)

// write sends a raw command packet to the desk (write-without-response).
func (d *Desk) write(pkt []byte) error {
	d.verboseLogf("[BLE tx] % X", pkt)
	_, err := d.cmdChar.WriteWithoutResponse(pkt)
	return err
}

// Wake sends the wake command (opcode 0x00) three times with 100 ms gaps
// between each write.
//
// Wake must be called before any motion command to ensure the desk's BLE
// adapter is awake and the motor driver is ready to respond.
func (d *Desk) Wake() error {
	pkt := protocol.MakeCommand(opWake)
	for i := 0; i < 3; i++ {
		if err := d.write(pkt); err != nil {
			return fmt.Errorf("wake: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// MoveUp sends a single "move up" pulse (opcode 0x01).
//
// Due to motor inertia, a single pulse causes approximately 17 mm of upward
// travel even after Stop is sent immediately after.  Use WaitForHeight for
// precise positioning; use MoveUp in a timed loop for duration-based movement.
func (d *Desk) MoveUp() error {
	return d.write(protocol.MakeCommand(opMoveUp))
}

// MoveDown sends a single "move down" pulse (opcode 0x02).
//
// Same inertia caveat as MoveUp: a single pulse causes approximately 17 mm of
// downward travel.
func (d *Desk) MoveDown() error {
	return d.write(protocol.MakeCommand(opMoveDown))
}

// Stop sends the stop command (opcode 0x2B).
func (d *Desk) Stop() error {
	return d.write(protocol.MakeCommand(opStop))
}

// RequestHeightLimits asks the desk to emit height-limit configuration
// (notification opcode 0x07) on FE62.
//
// On some firmware this also triggers a delayed (~4 s) opcode-0x01 height
// notification as a side effect, which is used by CurrentHeight's slow path.
// Note: on Yaasa firmware the height-limits notification may never arrive —
// see the package documentation.
func (d *Desk) RequestHeightLimits() error {
	return d.write(protocol.MakeCommand(opRequestHeightLimits))
}

// RequestStatus is kept as a backwards-compatible alias for older call sites.
// Deprecated: use RequestHeightLimits; this does not directly request height.
func (d *Desk) RequestStatus() error {
	return d.RequestHeightLimits()
}

// MoveToHeight sends a "move to absolute height" command (opcode 0x1B).
//
// The height is encoded as a big-endian uint16 in whole millimetres in the
// FE61 payload (MillimetreBytes encoding).  The desk treats this as a
// continuous direction command and must receive repeated pulses every ~200 ms
// to keep moving; WaitForHeight handles the pulsing automatically.
func (d *Desk) MoveToHeight(h Height) error {
	b := h.MillimetreBytes()
	return d.write(protocol.MakeCommand(opMoveTo, b[0], b[1]))
}

// GoPreset sends the go-to-preset command for preset n (1–2).
//
// Like MoveToHeight, the desk needs repeated pulses to continue moving to the
// preset; WaitForPreset handles the pulsing automatically.
func (d *Desk) GoPreset(n int) error {
	switch n {
	case 1:
		return d.write(protocol.MakeCommand(opPreset1))
	case 2:
		return d.write(protocol.MakeCommand(opPreset2))
	default:
		return fmt.Errorf("preset %d unsupported: uplift-ble documents move commands only for presets 1 and 2", n)
	}
}

// RequestSitStandTime asks the desk to emit its accumulated sit/stand time
// counters on FE62 (notification opcode 0xA2, 6-byte HH:MM:SS × 2 payload).
//
// The desk updates its counters in real time; each call returns the current
// second.  FetchSitStandTime wraps this with a listener and timeout.
func (d *Desk) RequestSitStandTime() error {
	return d.write(protocol.MakeCommand(opRequestSitStandTime))
}

// SavePreset saves the current height as preset n (1–2).
func (d *Desk) SavePreset(n int) error {
	switch n {
	case 1:
		return d.write(protocol.MakeCommand(opSavePreset1))
	case 2:
		return d.write(protocol.MakeCommand(opSavePreset2))
	default:
		return fmt.Errorf("preset %d unsupported: save is only implemented for presets 1 and 2", n)
	}
}
