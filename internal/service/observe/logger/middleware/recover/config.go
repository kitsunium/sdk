// Package recover — holds the Config struct consumed by NewWithConfig.
// Pulled into its own file so recover_sink.go stays focused on the Sink
// contract.
package recover

// Config tunes the recover sink at construction time. Every field is
// optional — the zero-value Config produces a sink with the documented
// defaults (no callback), so the panic-absorption behaviour is byte-for-byte
// identical to New until OnPanic is wired.
type Config struct {
	// OnPanic is invoked for every downstream panic the recover block
	// absorbs in Write / Flush / Close. Because this middleware converts a
	// panic into a typed Panicked error and a Logger that swallows handler
	// errors then discards it, an absorbed runtime bug (nil-deref, slice
	// OOB) is otherwise invisible — no crash, no log line, no signal. This
	// hook mirrors async's OnError and gives operators a path to emit a
	// metric or fall back to a secondary sink. The argument is the Panicked
	// error that was returned to the caller. nil disables the callback.
	// Finding V34.
	OnPanic func(err error)
}
