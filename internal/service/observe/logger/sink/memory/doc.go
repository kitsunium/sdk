// Package memory implements a Sink that retains a defensive snapshot of every
// RecordEvent it receives in a mutex-guarded slice. It is intended for tests
// that need to assert on what was logged — level, message, and attrs — without
// parsing an encoder's byte output. Records returns an independent copy of the
// buffer and Reset clears it; both are safe for concurrent use alongside Write.
//
// Package memory — defines the Memory sink, an in-memory Sink that buffers a
// defensive snapshot of every received RecordEvent for test assertions.
//
// Package memory — compile-time proof that *Memory satisfies the core Sink port.
package memory
