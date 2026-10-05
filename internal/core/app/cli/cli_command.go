package cli

// isGroup is CommandValue.IsGroup's body: decl_gen.go writes CommandValue.IsGroup, from the
// design, as one call of it.
func (c CommandValue) isGroup() bool {
	//: the constructor has already refused every other combination, so the
	//: presence of children is the whole question.
	return len(c.Commands) > 0
}
