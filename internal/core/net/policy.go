package net

// allow is PolicyFunc.Allow's body: decl_gen.go writes PolicyFunc.Allow, from the
// design, as one call of it.
func (f PolicyFunc) allow(req RequestValue) error {
	//: the function IS the policy — there is no state to consult.
	return f(req)
}
