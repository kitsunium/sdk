package writer

const (
	// ConsoleStderr writes to os.Stderr. It is the zero value, so a
	// ConsoleConfig{} targets stderr by default: a caller who has not named a
	// stream has not made a choice, and stdout may be the process's protocol
	// channel (ADR 0030). The order of this block is load-bearing — the zero
	// value is the contract, not the name that happens to come first.
	ConsoleStderr ConsoleStream = iota
	// ConsoleStdout writes to os.Stdout. Reachable only by naming it.
	ConsoleStdout
)
