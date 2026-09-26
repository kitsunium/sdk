// Package statemachine_test — the edges: what a machine finds when it opens,
// a pass stopped half-way, a store that fails under the loop, a state flipped
// while a transition holds the entity, and a notification given up on.
package statemachine_test

import (
	"context"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcstm "github.com/kitsunium/sdk/internal/service/statemachine"
)

// TestWhatIsDueAtOpeningFiresOnTheFirstPass pins the opening's agenda: a
// guard that already holds and a deadline already past are due at once, and a
// guard that panics while the machine opens is left for the loop, which
// reports it.
func TestWhatIsDueAtOpeningFiresOnTheFirstPass(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	past := start.Add(-time.Minute)
	store := newMemStore()
	store.put(t, Item{ID: "held", State: Live, Stock: 0})
	store.put(t, Item{ID: "past", State: Live, Stock: 2, Expires: &past})
	store.put(t, Item{ID: "later", State: Live, Stock: 2})
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk})
	next, err := m.Step(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if store.read(t, "held").State != Sold || store.read(t, "past").State != Expired || store.read(t, "later").State != Live {
		t.Fatalf("after the first pass: held %s, past %s, later %s", store.read(t, "held").State, store.read(t, "past").State, store.read(t, "later").State)
	}
	if !next.Equal(start.Add(time.Hour)) {
		t.Errorf("next = %v; want the one entity left, due an hour after the opening", next)
	}

	panicky := svcstm.NewMachineSpec(stateOf).Initial(Draft).When("go", Draft, Live, func(Item) bool { panic("a guard's bug") })
	store2 := newMemStore()
	store2.put(t, Item{ID: "a", State: Draft})
	m2 := open(t, panicky, &svcstm.Config[Item, State]{Store: store2, Clock: clk})
	if _, err := m2.Step(t.Context()); !errs.HasCode(err, svcstm.CodeFunctionPanicked) {
		t.Errorf("a guard panicking at opening, then in the loop = %v", err)
	}
}

// TestAPassStoppedHalfWayLosesNothing pins the requeue: a pass whose context
// has ended looks at nothing, and the next pass looks at everything it left.
func TestAPassStoppedHalfWayLosesNothing(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store := newMemStore()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk})
	goLive(t, m, Item{ID: "a", Stock: 1})
	goLive(t, m, Item{ID: "b", Stock: 1})
	update(t, store, "a", func(i *Item) { i.Stock = 0 })
	update(t, store, "b", func(i *Item) { i.Stock = 0 })
	for _, key := range []string{"a", "b"} {
		if err := m.Changed(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	stopped, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.Step(stopped); err != nil {
		t.Fatalf("a stopped pass = %v", err)
	}
	if store.read(t, "a").State != Live || store.read(t, "b").State != Live {
		t.Fatal("a stopped pass fired something")
	}
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.read(t, "a").State != Sold || store.read(t, "b").State != Sold {
		t.Errorf("the next pass lost what the stopped one left: a %s, b %s", store.read(t, "a").State, store.read(t, "b").State)
	}
}

// TestAStoreThatFailsUnderTheLoopIsRetriedAfterItsBackoff pins that a read
// failure is the entity's failure: reported, held back, then retried.
func TestAStoreThatFailsUnderTheLoopIsRetriedAfterItsBackoff(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store := newMemStore()
	var r reports
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk, Report: r.add})
	goLive(t, m, Item{ID: "a", Stock: 1})
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	clk.Set(start.Add(time.Hour))
	store.failWith(errBoom)
	if _, err := m.Step(t.Context()); !errs.HasCode(err, svcstm.CodeStoreFailed) {
		t.Fatalf("a pass over a failing store = %v", err)
	}
	if _, err := m.Step(t.Context()); err != nil || store.read(t, "a").State != Live {
		t.Fatalf("the entity was retried before its backoff: %v", err)
	}
	clk.Advance(time.Second)
	if _, err := m.Step(t.Context()); err != nil || store.read(t, "a").State != Expired {
		t.Fatalf("after the backoff: %v, state %s", err, store.read(t, "a").State)
	}
	if len(r.all()) != 1 {
		t.Errorf("reported %v", r.all())
	}
}

// TestAStorePanickingUnderTheLoopIsOneEntitysFailure pins LoopPanicked: the
// loop survives a panic of the caller's store, the other entity due in the same
// pass still fires, and the one whose read panicked is retried after its
// backoff — its lock was released on the way out.
func TestAStorePanickingUnderTheLoopIsOneEntitysFailure(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store := newMemStore()
	var r reports
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk, Report: r.add})
	goLive(t, m, Item{ID: "a", Stock: 1})
	goLive(t, m, Item{ID: "b", Stock: 1})
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	clk.Set(start.Add(time.Hour))
	store.panicOnce()
	if _, err := m.Step(t.Context()); !errs.HasCode(err, svcstm.CodeLoopPanicked) {
		t.Fatalf("a pass over a panicking store = %v", err)
	}
	if store.read(t, "a").State != Live || store.read(t, "b").State != Expired {
		t.Fatalf("after the panic: a %s, b %s", store.read(t, "a").State, store.read(t, "b").State)
	}
	clk.Advance(time.Second)
	if _, err := m.Step(t.Context()); err != nil || store.read(t, "a").State != Expired {
		t.Fatalf("after the backoff: %v, a %s", err, store.read(t, "a").State)
	}
	if reported := r.all(); len(reported) != 1 || !errs.HasCode(reported[0], svcstm.CodeLoopPanicked) {
		t.Errorf("reported %v", reported)
	}
}

// TestAStateFlippedDuringAFlightIsReadBeforeTheLockIsReleased pins the
// settling: a write that lands while a transition holds the entity is read
// again before the transition lets go, so the record and the census describe
// what the store holds.
func TestAStateFlippedDuringAFlightIsReadBeforeTheLockIsReleased(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store})
	if _, err := m.Start(t.Context(), Item{ID: "a", Stock: 1}); err != nil {
		t.Fatal(err)
	}
	// From now on, every write the machine makes is followed, inside the
	// flight, by one that moves the state behind its back.
	store.mu.Lock()
	store.notify = func(ctx context.Context, key string, deleted bool) {
		if deleted {
			return
		}
		store.mu.Lock()
		store.notify = nil
		store.mu.Unlock()
		update(t, store, key, func(i *Item) { i.State = Expired })
		if err := m.Changed(ctx, key); err != nil {
			t.Errorf("Changed() = %v", err)
		}
	}
	store.mu.Unlock()
	if _, err := m.Fire(t.Context(), "a", "publish"); err != nil {
		t.Fatal(err)
	}
	rec, _ := m.Record("a")
	census := m.Census()
	if rec.State != Expired || census[Expired] != 1 || census[Live] != 0 {
		t.Errorf("record %s, census %v; want what the store holds", rec.State, census)
	}
}

// TestANotificationGivenUpOnIsLeftForTheLoop pins Changed's own context: an
// ended one is WaitAbandoned, and the entity waits for the next pass.
func TestANotificationGivenUpOnIsLeftForTheLoop(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store := newMemStore()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk})
	goLive(t, m, Item{ID: "a", Stock: 1})
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	update(t, store, "a", func(i *Item) { i.Stock = 0 })
	stopped, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.Changed(stopped, "a"); !errs.HasCode(err, svcstm.CodeWaitAbandoned) {
		t.Fatalf("Changed with an ended context = %v", err)
	}
	if _, err := m.Step(t.Context()); err != nil || store.read(t, "a").State != Sold {
		t.Errorf("the pass after an abandoned notification: %v, state %s", err, store.read(t, "a").State)
	}
}

// TestTheLoopsWordsAreNamed pins the text of the loop's two enums.
func TestTheLoopsWordsAreNamed(t *testing.T) {
	t.Parallel()
	wakes := map[svcstm.Wake]string{svcstm.WakeStart: "start", svcstm.WakeDue: "due", svcstm.WakeChange: "change", 0: ""}
	for w, want := range wakes {
		if w.String() != want {
			t.Errorf("Wake(%d) = %q; want %q", w, w.String(), want)
		}
	}
	kinds := map[svcstm.LoopEventKind]string{
		svcstm.LoopRunStarted: "run-started", svcstm.LoopRunEnded: "run-ended", svcstm.LoopWaiting: "waiting", 0: "",
	}
	for k, want := range kinds {
		if k.String() != want {
			t.Errorf("LoopEventKind(%d) = %q; want %q", k, k.String(), want)
		}
	}
}
