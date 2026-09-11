// Package net — the WebSocket frame opcode (RFC 6455 §5.2).
package net

// wsControlBit is set on every control opcode (0x8..0xF) and clear on every
// data opcode (0x0..0x7), which is the whole reason the opcode space is split
// at 8 — an endpoint can classify an opcode it does not know.
const wsControlBit byte = 0x08

// The opcodes RFC 6455 defines. Everything else in the four-bit space is
// reserved, and a reserved opcode fails the connection rather than being
// skipped: an endpoint that ignored it would be desynchronised from a peer that
// believes the frame meant something.
const (
	// WSContinuation continues the message the previous frame began.
	WSContinuation WSOpCode = 0x0
	// WSText carries a UTF-8 payload, validated on both sides.
	WSText WSOpCode = 0x1
	// WSBinary carries opaque bytes.
	WSBinary WSOpCode = 0x2
	// WSClose begins or completes the closing handshake.
	WSClose WSOpCode = 0x8
	// WSPing asks the peer to answer with a Pong carrying the same payload.
	WSPing WSOpCode = 0x9
	// WSPong answers a Ping, or is sent unsolicited as a one-way heartbeat.
	WSPong WSOpCode = 0xA
)

// WSOpCode is a frame's four-bit type field (RFC 6455 §5.2).
type WSOpCode byte

// IsControl reports whether the opcode names a control frame.
//
// Control frames are the ones that may be injected BETWEEN the fragments of a
// message, so the distinction decides which fragmentation rules apply.
func (o WSOpCode) IsControl() bool {
	//: the opcode space is split at 8 precisely so this test is one bit.
	return byte(o)&wsControlBit != 0
}

// Defined reports whether RFC 6455 assigns this opcode a meaning.
func (o WSOpCode) Defined() bool {
	//: the six assigned opcodes; every other value in the four-bit space is
	//: reserved, and reserved means "fail the connection", not "ignore".
	switch o {
	//: the three data opcodes and the three control ones.
	case WSContinuation, WSText, WSBinary, WSClose, WSPing, WSPong:
		//: assigned by the RFC.
		return true
	//: 0x3-0x7 for data, 0xB-0xF for control.
	default:
		//: reserved.
		return false
	}
}

// String renders the opcode for a log line or an error field.
func (o WSOpCode) String() string {
	//: named where the RFC names it, and reported as reserved where it does
	//: not — an invented name would read like a protocol feature.
	switch o {
	//: opcode 0.
	case WSContinuation:
		//: the RFC's own name.
		return "continuation"
	//: opcode 1.
	case WSText:
		//: the RFC's own name.
		return "text"
	//: opcode 2.
	case WSBinary:
		//: the RFC's own name.
		return "binary"
	//: opcode 8.
	case WSClose:
		//: the RFC's own name.
		return "close"
	//: opcode 9.
	case WSPing:
		//: the RFC's own name.
		return "ping"
	//: opcode 10.
	case WSPong:
		//: the RFC's own name.
		return "pong"
	//: everything the RFC left unassigned.
	default:
		//: a reserved opcode has no name.
		return "reserved"
	}
}
