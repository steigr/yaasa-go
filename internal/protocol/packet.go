// Package protocol implements the Jiecang/Lierda FE60 BLE desk packet framing.
//
// Frame format (command, written to FE61):
//
//	F1 F1  [opcode]  [payloadLen]  [...payload]  [checksum]  7E
//
// Frame format (response, received from FE62):
//
//	F2 F2  [opcode]  [payloadLen]  [...payload]  [checksum]  7E
//
// Checksum = (opcode + payloadLen + sum(payload)) & 0xFF
package protocol

const (
	cmdStart  = 0xF1
	respStart = 0xF2
	frameEnd  = 0x7E
)

// Notification is one parsed FE62 notification packet.
type Notification struct {
	Opcode   byte
	Payload  []byte
	Checksum byte
}

// MakeCommand encodes a command packet for the given opcode and optional payload bytes.
func MakeCommand(opcode byte, payload ...byte) []byte {
	cs := checksum(opcode, payload)
	buf := make([]byte, 0, 4+len(payload)+1)
	buf = append(buf, cmdStart, cmdStart, opcode, byte(len(payload)))
	buf = append(buf, payload...)
	buf = append(buf, cs, frameEnd)
	return buf
}

// DecodeResponses scans a BLE attribute value for one or more valid FE62
// notification packets. It mirrors uplift-ble's parser: packets must use the
// F2 F2 header, a valid payload length, a valid checksum, and 0x7E terminator.
func DecodeResponses(buf []byte) []Notification {
	var out []Notification
	for i := 0; i+6 <= len(buf); {
		if buf[i] != respStart || buf[i+1] != respStart {
			i++
			continue
		}

		payloadLen := int(buf[i+3])
		totalLen := 2 + 1 + 1 + payloadLen + 1 + 1
		if i+totalLen > len(buf) {
			break
		}

		opcode := buf[i+2]
		payload := buf[i+4 : i+4+payloadLen]
		cs := buf[i+totalLen-2]
		if buf[i+totalLen-1] != frameEnd || checksum(opcode, payload) != cs {
			i++
			continue
		}

		out = append(out, Notification{Opcode: opcode, Payload: payload, Checksum: cs})
		i += totalLen
	}
	return out
}

// DecodeResponse parses the first valid notification packet received from FE62.
// Returns the opcode, payload slice, and whether parsing succeeded.
func DecodeResponse(buf []byte) (opcode byte, payload []byte, ok bool) {
	packets := DecodeResponses(buf)
	if len(packets) == 0 {
		return 0, nil, false
	}
	return packets[0].Opcode, packets[0].Payload, true
}

func checksum(opcode byte, payload []byte) byte {
	cs := opcode + byte(len(payload))
	for _, b := range payload {
		cs += b
	}
	return cs
}
