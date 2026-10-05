package net

// degraded is StateValue.Degraded's body: decl_gen.go writes StateValue.Degraded, from the
// design, as one call of it.
func (s StateValue) degraded() bool {
	//: any degraded listener degrades the whole server's answer.
	for _, listener := range s.Listeners {
		//: the first degraded listener settles it.
		if listener.Degraded {
			//: one degraded listener settles the whole answer.
			return true
		}
	}
	//: every listener got what it asked for.
	return false
}
