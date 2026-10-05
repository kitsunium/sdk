package net

// Degraded reports whether any listener fell back from a requested
// optimisation, so a caller can assert "no silent degradation" in one call
// instead of walking the slice.
func (s StateValue) Degraded() bool {
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
