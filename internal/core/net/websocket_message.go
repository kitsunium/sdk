package net

// opCode is WSMessageValue.OpCode's body: decl_gen.go writes WSMessageValue.OpCode, from the
// design, as one call of it.
func (m WSMessageValue) opCode() WSOpCode {
	//: the two data opcodes are the whole space a message can occupy.
	if m.Binary {
		//: opaque bytes.
		return WSBinary
	}
	//: UTF-8 text, the default.
	return WSText
}
