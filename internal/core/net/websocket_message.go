package net

// WSMessageValue is one complete WebSocket application message, reassembled
// from however many frames carried it.
//
// A message is the unit an application reasons about; a frame is the unit the
// transport reasons about. Handing the application frames would make
// fragmentation — which is the sender's private choice of chunk size, not a
// semantic boundary — visible in every consumer.
type WSMessageValue struct {
	// Binary reports whether the payload is opaque bytes (opcode 0x2) rather
	// than UTF-8 text (opcode 0x1). The zero value is text, which is the
	// opcode a zero-valued frame would carry anyway.
	Binary bool `json:"binary"`
	// Data is the reassembled payload. For a text message it is valid UTF-8,
	// which the engine verifies on both the receiving and the sending side.
	Data []byte `json:"data"`
}

// OpCode returns the frame opcode this message is carried under.
func (m WSMessageValue) OpCode() WSOpCode {
	//: the two data opcodes are the whole space a message can occupy.
	if m.Binary {
		//: opaque bytes.
		return WSBinary
	}
	//: UTF-8 text, the default.
	return WSText
}
