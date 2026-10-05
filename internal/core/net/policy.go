package net

// PolicyFunc adapts a plain function to the Policy interface.
type PolicyFunc func(req RequestValue) error

// Allow implements Policy by calling f.
func (f PolicyFunc) Allow(req RequestValue) error {
	//: the function IS the policy — there is no state to consult.
	return f(req)
}
