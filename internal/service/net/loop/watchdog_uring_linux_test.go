//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — io_uring's accept watchdog judges a listener, not a ring:
// a ring whose accepts lose the race to another ring's is not stalled.
package loop

import (
	"testing"
)

// TestWatchdog_otherRingAccepts holds a connection in a listener's queue and
// a ring whose armed accepts never complete. While another ring of the group
// accepts on the listener every window, the watchdog never fires; once no
// ring accepts, it re-arms watchdogFires times and then drains the ring.
func TestWatchdog_otherRingAccepts(t *testing.T) {
	lfd, addr := listenFD(t)
	t.Cleanup(func() { closeFDs([]int{lfd}) })
	_ = dial(t, addr)
	waitFor(t, "a connection in the queue", func() bool { return listenerQueue(lfd) > 0 })
	set := newListenerSet([]int{lfd}, 2)
	u, step, errno := openURing(probeEntries, 0)
	if errno != 0 {
		t.Skipf("io_uring %s: %v", step, errno)
	}
	t.Cleanup(u.close)
	lp := &loop{tick: 1}
	ul := &uLoop{lp: lp, lfds: []int{lfd}, set: set}
	rr := &uRing{ul: ul, u: u, armed: []int{acceptDepth}, seen: make([]uint64, 1)}
	rr.r = newRing(lp, 1, rr)
	for w := range 2 * watchdogFires {
		set.accepted(0)
		lp.tick += watchdogTicks
		rr.watchdog()
		if rr.r.st.watchdog != 0 || rr.r.draining {
			t.Fatalf("window %d: %d fires, draining %v, while another ring accepts", w, rr.r.st.watchdog, rr.r.draining)
		}
	}
	for w := range watchdogFires + 1 {
		lp.tick += watchdogTicks
		rr.watchdog()
		if w < watchdogFires && rr.r.draining {
			t.Fatalf("window %d: drained before %d re-arms", w, watchdogFires)
		}
	}
	if !rr.r.draining || rr.r.st.watchdog != uint64(watchdogFires+1) {
		t.Fatalf("no accept anywhere: %d fires, draining %v, want %d and true", rr.r.st.watchdog, rr.r.draining, watchdogFires+1)
	}
}

// TestCompactClosing_bounded pins the backstop queue of a ring that never
// empties it: the looked-at prefix moves out once it is closingCompact
// entries and most of the queue, the rest kept in order.
func TestCompactClosing_bounded(t *testing.T) {
	rr := &uRing{}
	for i := range 3 * closingCompact {
		rr.closing = append(rr.closing, closeWait{slot: int32(i)})
	}
	rr.closingHead = closingCompact / 2
	rr.compactClosing()
	if rr.closingHead != closingCompact/2 || len(rr.closing) != 3*closingCompact {
		t.Fatalf("a small prefix: head %d, %d queued, want it left alone", rr.closingHead, len(rr.closing))
	}
	rr.closingHead = 2 * closingCompact
	rr.compactClosing()
	if rr.closingHead != 0 || len(rr.closing) != closingCompact || rr.closing[0].slot != int32(2*closingCompact) {
		t.Fatalf("most of the queue looked at: head %d, %d queued, first slot %d", rr.closingHead, len(rr.closing), rr.closing[0].slot)
	}
	rr.closingHead = len(rr.closing)
	rr.compactClosing()
	if rr.closingHead != 0 || len(rr.closing) != 0 {
		t.Fatalf("every one looked at: head %d, %d queued, want an empty queue", rr.closingHead, len(rr.closing))
	}
}
