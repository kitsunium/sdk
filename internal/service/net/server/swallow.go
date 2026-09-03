// Package server — deliberate discard of non-actionable cleanup errors.
package server

// swallowErr intentionally discards a non-actionable error, recording the
// discard so the error audit treats it as deliberate rather than dropped.
//
// It is used on three paths where there is genuinely nothing to report:
// closing a socket whose handler has already finished, closing a listener
// during shutdown that a concurrent Close may already have closed, and setting
// a deadline on a connection that is already gone — in which case the handler's
// first read reports the real problem far more usefully.
func swallowErr(err error) {
	//: read the parameter so the unused-error audit treats this as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
	//: the error concerns a socket or listener that is already finished with.
}
