package proc

// success is ExitValue.Success's body: decl_gen.go writes ExitValue.Success, from the
// design, as one call of it.
func (e ExitValue) success() bool {
	//: a clean exit is a normal (non-signalled) termination with status zero.
	return !e.Signaled && e.Code == 0
}
