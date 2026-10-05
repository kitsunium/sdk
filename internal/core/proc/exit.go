package proc

// Success reports whether the process exited normally with status zero.
func (e ExitValue) Success() bool {
	//: a clean exit is a normal (non-signalled) termination with status zero.
	return !e.Signaled && e.Code == 0
}
