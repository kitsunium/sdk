// Package loop — the drain's take out of descriptors: it waits for a close
// only while the group holds a connection whose close frees one.
package loop

import (
	"syscall"
	"testing"
)

// TestTakeAfter_outOfDescriptors pins epoll's rule: EMFILE or ENFILE waits for
// the next batch while the group holds a connection, and ends the take once
// it holds none — no close would ever free a descriptor.
func TestTakeAfter_outOfDescriptors(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EMFILE, syscall.ENFILE} {
		if got := takeAfter(errno, 1); got != takeLater {
			t.Errorf("takeAfter(%v, 1) = %d, want takeLater", errno, got)
		}
		if got := takeAfter(errno, 0); got != takeOver {
			t.Errorf("takeAfter(%v, 0) = %d, want takeOver", errno, got)
		}
	}
	//: the other outcomes do not depend on the group's connections.
	if got := takeAfter(syscall.EAGAIN, 0); got != takeOver {
		t.Errorf("takeAfter(EAGAIN, 0) = %d, want takeOver", got)
	}
	if got := takeAfter(syscall.ECONNABORTED, 0); got != takeNext {
		t.Errorf("takeAfter(ECONNABORTED, 0) = %d, want takeNext", got)
	}
}
