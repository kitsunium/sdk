// Package loop — the drain's take out of descriptors: it waits for a close
// only while the group holds a connection whose close frees one.
package loop

import (
	"syscall"
	"testing"
)

// TestTakeAfter_outOfDescriptors pins epoll's rule: EMFILE or ENFILE waits for
// the next batch while the group holds a connection, and ends the take once
// it holds none — no close would ever free a descriptor. The other outcomes
// do not depend on the group's connections.
func TestTakeAfter_outOfDescriptors(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// err is accept4's failure.
		err syscall.Errno
		// live is the connections the group still holds.
		live int64
		// want is the take's outcome.
		want uint8
	}
	tests := []tc{
		{name: "EMFILE with a connection left waits", err: syscall.EMFILE, live: 1, want: takeLater},
		{name: "EMFILE with none left ends the take", err: syscall.EMFILE, live: 0, want: takeOver},
		{name: "ENFILE with a connection left waits", err: syscall.ENFILE, live: 1, want: takeLater},
		{name: "ENFILE with none left ends the take", err: syscall.ENFILE, live: 0, want: takeOver},
		{name: "EAGAIN ends the take", err: syscall.EAGAIN, live: 0, want: takeOver},
		{name: "ECONNABORTED skips one", err: syscall.ECONNABORTED, live: 0, want: takeNext},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the outcome for this failure and this count.
		if got := takeAfter(c.err, c.live); got != c.want {
			t.Errorf("takeAfter(%v, %d) = %d, want %d", c.err, c.live, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
