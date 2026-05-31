// Package memory implements a Sink that retains a defensive snapshot of every
// RecordEvent it receives in a mutex-guarded slice. It is intended for tests
// that need to assert on what was logged — level, message, and attrs — without
// parsing an encoder's byte output. Records returns an independent copy of the
// buffer and Reset clears it; both are safe for concurrent use alongside Write.
package memory
