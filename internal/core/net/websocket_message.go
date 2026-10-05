package net

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
