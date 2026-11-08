package pool

// Config sizes a Pool and names the observers of what its tasks cannot
// return. The zero Config is usable: one worker, no queue, a task's error
// discarded and its panic raised again.
type Config struct {
	// Workers is how many tasks run at once, each on a long-lived worker; a
	// value below 1 is 1.
	Workers int
	// Queue is how many submitted tasks may wait for a worker. Zero is no
	// queue: Submit refuses at once when every worker is busy. A positive
	// queue makes a Submit that finds it full wait, within its context. A
	// negative value is zero.
	Queue int
	// OnPanic receives a task's panic, recovered on its worker, which then
	// serves the next task. Nil raises the panic again on the worker: a fault
	// nobody observes is not swallowed, and the process ends as it would for
	// a bare goroutine.
	OnPanic func(p PanicValue)
	// OnError receives a task's non-nil error, which has no caller to return
	// to. Nil discards it.
	OnError func(err error)
}
