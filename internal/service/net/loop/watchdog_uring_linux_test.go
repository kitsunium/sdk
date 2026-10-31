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
