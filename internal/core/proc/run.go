package proc

import "time"

// RunValue configures Run: how much of each output stream it keeps, and how long
// a cancelled run's child is given to stop.
type RunValue struct {
	// MaxOutputBytes is the number of bytes kept from each of stdout and stderr.
	// The child's output past it is read and discarded, so the child never blocks
	// on a full pipe and the caller never holds more. It must be positive: Run
	// refuses zero with InvalidSpec (ADR 0031).
	MaxOutputBytes int
	// Grace is how long a child whose run was cancelled is given between SIGTERM
	// and SIGKILL; zero or less escalates at once.
	Grace time.Duration
}

// ResultValue is what Run returns: the child's exit and the head of each of its
// output streams.
type ResultValue struct {
	// Exit is the child's ExitValue.
	Exit ExitValue
	// Stdout is the first RunValue.MaxOutputBytes bytes the child wrote to its standard output.
	Stdout []byte
	// Stderr is the first RunValue.MaxOutputBytes bytes the child wrote to its standard error.
	Stderr []byte
	// Truncated reports whether either stream wrote more than MaxOutputBytes, the
	// rest of it read and discarded.
	Truncated bool
}
