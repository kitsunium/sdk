// Package statemachine_test — the edges: what a machine finds when it opens,
// a pass stopped half-way, a store that fails under the loop, a state flipped
// while a transition holds the entity, a notification given up on, a journal
// that is slow or reads the machine, an observer that panics, and entities
// deleted under a read or a write.
package statemachine_test

import (
	"context"
	"slices"
	"sync"
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

// TestAMachineWithNothingAutomaticNeverRuns pins that Run makes no run at all
// when no timer and no guard is declared, and still returns when told to: it
// runs Run on a background goroutine and cancels its context to end it.
func TestAMachineWithNothingAutomaticNeverRuns(t *testing.T) {
	t.Parallel()
	onLoop, events := loopEvents()
	def := svcstm.NewMachineSpec(stateOf).Initial(Draft).On("go", Draft, Live)
	m := open(t, def, &svcstm.Config[Item, State]{Store: newMemStore(), OnLoop: onLoop})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	if _, err := m.Start(t.Context(), Item{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if n := count(events, svcstm.LoopRunStarted); n != 0 {
		t.Errorf("a machine with nothing automatic ran %d times", n)
	}
}

// TestReportMayCallTheMachine pins that Config.Report is called with no lock
// of the machine held: a Report that reads the census while a journal write
// fails — reported from inside a transition — and while the loop reports a
// failed entity, does not deadlock. Each call runs on a background goroutine
// the test bounds.
func TestReportMayCallTheMachine(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store, journal := newMemStore(), newMemJournal()
	var m *svcstm.StateMachine[Item, State]
	reported := make(chan int, 16)
	def := lifecycle().OnEnter(Expired, func(context.Context, *Item) error { return errBoom })
	m = open(t, def, &svcstm.Config[Item, State]{
		Store: store, Journal: journal, Clock: clk,
		Report: func(context.Context, error) { reported <- m.Census()[Live] },
	})
	journal.mu.Lock()
	journal.failSave = errBoom
	journal.mu.Unlock()
	if err := within(t, "Start", func() error { _, err := m.Start(context.Background(), Item{ID: "a", Stock: 1}); return err }); err != nil {
		t.Fatalf("Start: %v; a journal failure is reported, not returned", err)
	}
	if err := within(t, "Fire", func() error { _, err := m.Fire(context.Background(), "a", "publish"); return err }); err != nil {
		t.Fatalf("Fire: %v; a journal failure is reported, not returned", err)
	}
	clk.Set(start.Add(time.Hour))
	if err := within(t, "Step", func() error { _, err := m.Step(context.Background()); return err }); !errs.HasCode(err, svcstm.CodeHookFailed) {
		t.Fatalf("Step: %v; want the OnEnter hook's failure", err)
	}
	if len(reported) < 3 {
		t.Errorf("%d reports; want the two journal failures and the loop's", len(reported))
	}
}

// TestAJournalWriteHoldsOnlyItsOwnKey pins the journal's order without the
// book's mutex: while a write of one key is in the journal, a transition of
// another key, the census and the records go on; a deletion of the same key
// waits for the write and lands after it, so the journal never keeps the
// record of an entity deleted meanwhile. The creation and the deletion run on
// background goroutines that return once the journal lets the write go; the
// test waits for both to end.
func TestAJournalWriteHoldsOnlyItsOwnKey(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	journal := &hookedJournal{memJournal: newMemJournal(), before: func(op string, keys []string) {
		if op == "save" && slices.Contains(keys, "a") {
			once.Do(func() { close(entered) })
			<-release
		}
	}}
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: newMemStore(), Journal: journal})
	if _, err := m.Start(t.Context(), Item{ID: "b", Stock: 1}); err != nil {
		t.Fatal(err)
	}
	created := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), Item{ID: "a", Stock: 1}); created <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("a's creation never reached the journal")
	}
	if err := within(t, "a transition of another key", func() error {
		_, err := m.Fire(context.Background(), "b", "publish")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := within(t, "the census and the records", func() error { m.Census(); m.Records(); return nil }); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- m.Deleted(context.Background(), "a") }()
	select {
	case err := <-deleted:
		t.Fatalf("Deleted returned (%v) while a's own write was in the journal", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for _, done := range []chan error{created, deleted} {
		if err := within(t, "a's creation and deletion", func() error { return <-done }); err != nil {
			t.Fatal(err)
		}
	}
	if _, kept := journal.record("a"); kept {
		t.Error("the journal keeps a deleted entity: its delete overtook its save")
	}
	if _, known := m.Record("a"); known {
		t.Error("the machine keeps a deleted entity's record")
	}
}

// TestAJournalMayReadTheMachine pins that no journal call is made under the
// book's mutex: a journal that reads the census and the records on every call
// does not deadlock a creation, a transition or a deletion.
func TestAJournalMayReadTheMachine(t *testing.T) {
	t.Parallel()
	var m *svcstm.StateMachine[Item, State]
	journal := &hookedJournal{memJournal: newMemJournal(), before: func(string, []string) {
		if m != nil {
			m.Census()
			m.Records()
		}
	}}
	m = open(t, lifecycle(), &svcstm.Config[Item, State]{Store: newMemStore(), Journal: journal})
	ctx := context.Background()
	steps := []struct {
		call func() error
		what string
	}{
		{what: "Start", call: func() error { _, err := m.Start(ctx, Item{ID: "a", Stock: 1}); return err }},
		{what: "Fire", call: func() error { _, err := m.Fire(ctx, "a", "publish"); return err }},
		{what: "Deleted", call: func() error { return m.Deleted(ctx, "a") }},
	}
	for _, step := range steps {
		if err := within(t, step.what, step.call); err != nil {
			t.Fatalf("%s: %v", step.what, err)
		}
	}
}

// TestAnObserversEndThatPanicsChangesNothing pins the order of fire: the
// agenda records a stored transition before the observer hears of it, so the
// observer's panic is reported — LoopPanicked, call=observe-end — and neither
// fails the pass nor schedules a retry of what was stored.
func TestAnObserversEndThatPanicsChangesNothing(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store := newMemStore()
	var r reports
	observe := func(ctx context.Context, _ svcstm.FiringValue[State]) (context.Context, func(error)) {
		return ctx, func(error) { panic("an observer's bug") }
	}
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk, Report: r.add, Observe: observe})
	goLive(t, m, Item{ID: "a", Stock: 1})
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	clk.Set(start.Add(time.Hour))
	next, err := m.Step(t.Context())
	if err != nil || store.read(t, "a").State != Expired {
		t.Fatalf("Step() = %v, state %s; want the transition stored and no failure", err, store.read(t, "a").State)
	}
	if !next.IsZero() {
		t.Errorf("next = %v; want nothing scheduled — a stored transition is not retried", next)
	}
	reported := r.all()
	if len(reported) != 1 || !errs.HasCode(reported[0], svcstm.CodeLoopPanicked) || !hasField(reported[0], "call", "observe-end") {
		t.Fatalf("reported %v; want the observer's panic, once", reported)
	}
	if rec, _ := m.Record("a"); len(rec.History) != 3 {
		t.Errorf("history = %+v; want create, publish, expire", rec.History)
	}
}

// hasField reports whether err carries the field key with value.
func hasField(err error, key, value string) bool {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key && f.StringValue() == value {
			return true
		}
	}
	return false
}

// TestAnEntityGoneUnderTheLoopIsForgotten pins the loop's rule for an entity
// its store no longer holds when the transition writes it — deleted with no
// word to the machine: forgotten everywhere, record, census and journal, as a
// read that finds nothing, and not a failure.
func TestAnEntityGoneUnderTheLoopIsForgotten(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store, journal := newMemStore(), newMemJournal()
	def := lifecycle().OnEnter(Expired, func(ctx context.Context, i *Item) error {
		store.remove(ctx, i.ID)
		return nil
	})
	m := open(t, def, &svcstm.Config[Item, State]{Store: store, Journal: journal, Clock: clk})
	goLive(t, m, Item{ID: "a", Stock: 1})
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	clk.Set(start.Add(time.Hour))
	next, err := m.Step(t.Context())
	if err != nil || !next.IsZero() {
		t.Fatalf("Step() = %v, %v; want no failure and nothing scheduled", next, err)
	}
	if _, known := m.Record("a"); known {
		t.Error("the machine keeps the record of an entity its store no longer holds")
	}
	if census := m.Census(); census[Live] != 0 || census[Expired] != 0 {
		t.Errorf("census = %v", census)
	}
	if _, kept := journal.record("a"); kept {
		t.Error("the journal keeps the record of an entity its store no longer holds")
	}
}

// TestADeletionDuringAReadIsNotUndone pins the read's flight: a deletion that
// lands after the store answered and before the machine recorded the answer
// wins, for the loop's read and for a Changed's alike — the record, the
// census and the journal do not bring the entity back.
func TestADeletionDuringAReadIsNotUndone(t *testing.T) {
	t.Parallel()
	type tc struct {
		read func(ctx context.Context, m *svcstm.StateMachine[Item, State]) error
		name string
	}
	cases := []tc{
		{name: "the loop's read", read: func(ctx context.Context, m *svcstm.StateMachine[Item, State]) error {
			_, err := m.Step(ctx)
			return err
		}},
		{name: "a Changed's read", read: func(ctx context.Context, m *svcstm.StateMachine[Item, State]) error {
			return m.Changed(ctx, "a")
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		store, journal := newMemStore(), newMemJournal()
		def := svcstm.NewMachineSpec(stateOf).Initial(Live).When("sold-out", Live, Sold, soldOut)
		m := open(t, def, &svcstm.Config[Item, State]{Store: store, Journal: journal, Clock: clock.NewManualClock(start)})
		if _, err := m.Start(t.Context(), Item{ID: "a", Stock: 5}); err != nil {
			t.Fatal(err)
		}
		store.onNextGet(func(ctx context.Context, key string) {
			store.remove(ctx, key)
			if err := m.Deleted(ctx, key); err != nil {
				t.Errorf("Deleted: %v", err)
			}
		})
		if err := c.read(t.Context(), m); err != nil {
			t.Fatal(err)
		}
		if _, known := m.Record("a"); known {
			t.Error("the read brought back the record of an entity deleted after it")
		}
		if census := m.Census(); census[Live] != 0 {
			t.Errorf("census = %v", census)
		}
		if _, kept := journal.record("a"); kept {
			t.Error("the read wrote a deleted entity back to the journal")
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAHookErrorCarryingEntityMissingIsAFailure pins that only the store's own
// refusal reads as a deletion: an OnEnter hook whose error carries the
// ENTITY_MISSING code — another machine's refusal — fails the transition, the
// entity is kept, and it is retried after its backoff.
func TestAHookErrorCarryingEntityMissingIsAFailure(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store := newMemStore()
	elsewhere := errs.Wrap(svcstm.EntityMissing, errs.WrapParams{}, errs.String("key", "elsewhere"))
	def := lifecycle().OnEnter(Expired, func(context.Context, *Item) error { return elsewhere })
	m := open(t, def, &svcstm.Config[Item, State]{Store: store, Clock: clk})
	goLive(t, m, Item{ID: "a", Stock: 1})
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	clk.Set(start.Add(time.Hour))
	next, err := m.Step(t.Context())
	if !errs.HasCode(err, svcstm.CodeHookFailed) {
		t.Fatalf("Step() = %v; want the hook's failure", err)
	}
	if _, known := m.Record("a"); !known || store.read(t, "a").State != Live {
		t.Error("an entity its store still holds was forgotten")
	}
	if want := start.Add(time.Hour + time.Second); !next.Equal(want) {
		t.Errorf("next = %v; want the retry after the backoff, %v", next, want)
	}
}
