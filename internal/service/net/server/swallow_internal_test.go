// Package server — deliberate discard of non-actionable cleanup errors.
package server

import (
	"errors"
	"testing"
)

// Test_swallowErr pins that discarding is TOTAL and silent.
//
// The helper exists so the error audit can tell a deliberate discard from a
// dropped one, and it is used on three paths where there is genuinely nothing
// to report: closing a socket whose handler has already finished, closing a
// listener a concurrent Close may have closed, and setting a deadline on a
// connection that is already gone. On each of those a panic, a log line or a
// re-thrown error would turn a non-event into an incident — which is why the
// only thing worth asserting is that nothing at all happens.
func Test_swallowErr(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// err is what the caller is discarding.
		err error
	}
	tests := []tc{
		{name: "nothing went wrong"},
		{name: "a socket already closed", err: errors.New("use of closed network connection")},
		{name: "an invalid deadline on a dead connection", err: errors.New("set deadline: file already closed")},
		{name: "a wrapped failure", err: errors.Join(errors.New("close"), errors.New("listener"))},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: reaching the assertion below at all is the property: the helper must
		//: never panic and must never propagate.
		swallowErr(c.err)

		if t.Failed() {
			t.Fatalf("discarding %v was not silent", c.err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
