package cli

// IsGroup reports whether this command dispatches to children rather than
// running an [Action]. It reads the declaration rather than a flag set at
// construction, so it answers the same on a CommandValue the caller has just
// written and on one the engine has already validated.
func (c CommandValue) IsGroup() bool {
	//: the constructor has already refused every other combination, so the
	//: presence of children is the whole question.
	return len(c.Commands) > 0
}
