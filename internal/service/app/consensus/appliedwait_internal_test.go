package consensus

import (
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// bareApplyNode is a Node holding only what applied1 and waitApplied touch.
func bareApplyNode() *Node {
	return &Node{waiters: map[uint64]*proposal{}, loopDone: make(chan struct{})}
}

// parked is how many callers wait in n's queue.
func parked(n *Node) int {
	n.am.Lock()
	defer n.am.Unlock()
	if n.appliedQ == nil {
		return 0
	}
	return n.appliedQ.Len()
}

// awaitParked waits until want callers wait in n's queue.
func awaitParked(t *testing.T, n *Node, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for parked(n) != want {
		if time.Now().After(deadline) {
			t.Fatalf("%d callers parked, want %d", parked(n), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// applyUpTo applies the entries from lo to hi, both included, at term 1.
func applyUpTo(n *Node, lo, hi uint64) {
	for i := lo; i <= hi; i++ {
		e := coreconsensus.EntryValue{Position: coreconsensus.PositionValue{Term: 1, Index: i}}
		n.applied1(&e, nil)
	}
}

// TestWaitAppliedReleasesNoCallerBeforeItsIndex is #1109: applied1 woke
// every parked caller through a Broadcast on acond, each re-checking its
// index under am and parking again, R callers times E entries. 1 000
// callers parked on 1 000..1 999 see 999 entries applied below them with no
// release, then each is released once, by the entry at its index.
func TestWaitAppliedReleasesNoCallerBeforeItsIndex(t *testing.T) {
	if _, ok := reflect.TypeFor[Node]().FieldByName("acond"); ok {
		t.Fatal("Node still holds acond")
	}
	n := bareApplyNode()
	var released, spurious atomic.Int64
	n.onRelease = func(want, applied uint64) {
		released.Add(1)
		if want > applied {
			spurious.Add(1)
		}
	}
	const callers = 1000
	var wg sync.WaitGroup
	var unreleased atomic.Int64
	for i := range uint64(callers) {
		wg.Go(func() {
			if !n.waitApplied(nil, callers+i) {
				unreleased.Add(1)
			}
		})
	}
	awaitParked(t, n, callers)
	applyUpTo(n, 1, callers-1)
	if got := released.Load(); got != 0 {
		t.Fatalf("999 entries below 1 000 parked callers released %d of them, want 0", got)
	}
	if got := parked(n); got != callers {
		t.Fatalf("%d callers still parked after 999 entries below them, want %d", got, callers)
	}
	applyUpTo(n, callers, 2*callers-1)
	wg.Wait()
	if got := released.Load(); got != callers {
		t.Errorf("%d releases for %d callers, want one each", got, callers)
	}
	if got := spurious.Load(); got != 0 {
		t.Errorf("%d callers released before their index was applied, want 0", got)
	}
	if got := unreleased.Load(); got != 0 {
		t.Errorf("%d callers returned false, want every one applied", got)
	}
}

// TestWaitAppliedLaunchesNoGoroutine is #1109: waitApplied launched one
// helper goroutine per call, to turn done or the loop's end into a
// Broadcast, since a sync.Cond cannot select. A parked caller now selects
// on its own release: while one waits, the process holds its goroutine and
// no other, and 100 calls that returned, cancelled or released, leave the
// goroutine count and the queue as they found them.
func TestWaitAppliedLaunchesNoGoroutine(t *testing.T) {
	n := bareApplyNode()
	before := settledGoroutines()
	ret := make(chan bool, 1)
	go func() { ret <- n.waitApplied(nil, 1) }()
	awaitParked(t, n, 1)
	if got := runtime.NumGoroutine(); got > before+1 {
		t.Errorf("one parked caller: %d goroutines, want at most %d (the caller's alone)", got, before+1)
	}
	applyUpTo(n, 1, 1)
	if !<-ret {
		t.Fatal("the caller parked on index 1 returned false once it was applied")
	}

	cancelled := make(chan struct{})
	close(cancelled)
	before = settledGoroutines()
	for i := range uint64(100) {
		switch i % 3 {
		case 0: // already applied
			if !n.waitApplied(nil, 1) {
				t.Fatal("waitApplied on an applied index returned false")
			}
		case 1: // gives up
			if n.waitApplied(cancelled, 1000) {
				t.Fatal("waitApplied on an index never applied returned true")
			}
		default: // released
			go applyUpTo(n, 2+i, 2+i)
			if !n.waitApplied(nil, 2+i) {
				t.Fatal("waitApplied on an index applied meanwhile returned false")
			}
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before {
		t.Errorf("after 100 calls returned: %d goroutines, want at most %d", got, before)
	}
	if got := parked(n); got != 0 {
		t.Errorf("%d waiters left in the queue after every call returned, want 0", got)
	}
}

// settledGoroutines is the goroutine count once it held still for 50 ms,
// within 10 s: the goroutines an earlier test of the suite left winding down
// exit before the count is taken, so they are not read as this test's. The
// counts after it are bounded above, never matched exactly, since such a
// goroutine may still exit meanwhile.
func settledGoroutines() int {
	deadline := time.Now().Add(10 * time.Second)
	n, still := runtime.NumGoroutine(), 0
	for still < 50 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		if m := runtime.NumGoroutine(); m != n {
			n, still = m, 0
			continue
		}
		still++
	}
	return n
}

// TestWaitAppliedReturnsOnTheLoopsEnd: a caller parked on an index never
// applied returns false once the loop ended, and leaves the queue.
func TestWaitAppliedReturnsOnTheLoopsEnd(t *testing.T) {
	n := bareApplyNode()
	ret := make(chan bool, 1)
	go func() { ret <- n.waitApplied(nil, 5) }()
	awaitParked(t, n, 1)
	close(n.loopDone)
	if <-ret {
		t.Fatal("a caller parked on an index never applied returned true once the loop ended")
	}
	if got := parked(n); got != 0 {
		t.Errorf("%d waiters left in the queue, want 0", got)
	}
}

// TestTheResultRingAnswersOnlyItsOwnIndex is #1109: a forward answered
// after its index was applied is answered from the ring slot of its index,
// and only while the slot still holds that index: once resultRing later
// entries overwrote it, the answer is OUTCOME_UNKNOWN as before.
func TestTheResultRingAnswersOnlyItsOwnIndex(t *testing.T) {
	n := bareApplyNode()
	n.keepResults = 1
	n.results = new([resultRing]applyResult)
	for i := uint64(1); i <= resultRing+10; i++ {
		e := coreconsensus.EntryValue{Position: coreconsensus.PositionValue{Term: 2, Index: i}}
		n.applied1(&e, i)
	}
	n.am.Lock()
	defer n.am.Unlock()
	if r, ok := n.keptResult(resultRing + 5); !ok || r.term != 2 || r.val != uint64(resultRing+5) {
		t.Errorf("keptResult(%d) = %+v, %v, want term 2 and its value", resultRing+5, r, ok)
	}
	if _, ok := n.keptResult(5); ok {
		t.Error("keptResult(5) answered from a slot a later entry overwrote")
	}
	if _, ok := n.keptResult(resultRing + 11); ok {
		t.Error("keptResult answered an index never applied")
	}
	if _, ok := n.keptResult(0); ok {
		t.Error("keptResult answered index 0 from a slot never written")
	}
}
