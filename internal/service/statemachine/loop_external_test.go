// Package statemachine_test — the machine's own loop: timers, deadlines and
// guards, the first-declared rule, the per-entity backoff, the journal across
// a restart, and a loop that sleeps until something is due.
package statemachine_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcstm "github.com/kitsunium/sdk/internal/service/statemachine"
)

// goLive creates an item and publishes it.
func goLive(t *testing.T, m *svcstm.StateMachine[Item, State], i Item) {
	t.Helper()
	if _, err := m.Start(t.Context(), i); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Fire(t.Context(), i.ID, "publish"); err != nil {
		t.Fatal(err)
	}
}

// update rewrites a stored item through the store, as code that bypasses the
// machine does.
func update(t *testing.T, store *memStore, key string, change func(*Item)) {
	t.Helper()
	i := store.read(t, key)
	change(&i)
	store.put(t, i)
}

// TestTheLoopFiresGuardsOnAWriteAndTimersWhenDue is kit's own workflow test,
// on the engine: a write makes a guard hold and wakes the loop; a deadline the
// entity carries and a duration in the state fire when they come; in between,
// with nothing due, the loop does not run at all.
func TestTheLoopFiresGuardsOnAWriteAndTimersWhenDue(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store := newMemStore()
	onLoop, events := loopEvents()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk, OnLoop: onLoop})
	wire(t, store, m)
	running(t, m)
	state := func(key string) State { return store.read(t, key).State }

	lapse := start.Add(30 * time.Minute)
	goLive(t, m, Item{ID: "timer", Stock: 3})
	goLive(t, m, Item{ID: "lapse", Stock: 3, Expires: &lapse})
	goLive(t, m, Item{ID: "guard", Stock: 1})
	update(t, store, "guard", func(i *Item) { i.Stock = 0 })

	eventually(t, clk, "the guard", func() bool { return state("guard") == Sold })
	if state("timer") != Live || state("lapse") != Live {
		t.Fatalf("a timer fired early: %s, %s", state("timer"), state("lapse"))
	}
	awaitRun(t, clk, events, "the loop asleep until the lapse", func(e svcstm.LoopEvent) bool { return e.Next.Equal(lapse) })

	drain(events)
	for range 10 {
		clk.Advance(time.Minute)
	}
	time.Sleep(20 * time.Millisecond)
	if n := count(events, svcstm.LoopRunStarted); n != 0 {
		t.Fatalf("the loop ran %d times with nothing due", n)
	}

	clk.Set(lapse)
	eventually(t, nil, "the lapse", func() bool { return state("lapse") == Expired })
	if state("timer") != Live {
		t.Fatal("the delay timer fired at the deadline")
	}
	clk.Set(start.Add(time.Hour))
	eventually(t, nil, "the delay", func() bool { return state("timer") == Expired })
	awaitRun(t, clk, events, "the loop asleep with nothing ahead", func(e svcstm.LoopEvent) bool { return e.Next.IsZero() })

	for key, want := range map[string]corestm.Trigger{"guard": corestm.TriggerGuard, "lapse": corestm.TriggerDeadline, "timer": corestm.TriggerDelay} {
		rec, _ := m.Record(key)
		if last := rec.History[len(rec.History)-1]; last.Trigger != want || last.Actor != "" {
			t.Errorf("%s's last step = %+v; want trigger %s and no actor", key, last, want)
		}
	}
}

// TestTheFirstDeclaredTransitionDueFires pins the rule kit's sweep had: when
// several automatic transitions of one state are due, the first declared wins.
func TestTheFirstDeclaredTransitionDueFires(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store := newMemStore()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk})
	goLive(t, m, Item{ID: "a", Stock: 1})
	clk.Advance(2 * time.Hour)
	update(t, store, "a", func(i *Item) { i.Stock = 0 })
	if err := m.Changed(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	// expire (After, declared third) and sold-out (When, declared fifth) are
	// both due: expire wins.
	if got := store.read(t, "a"); got.State != Expired {
		t.Fatalf("state %s; want the first declared transition, expire", got.State)
	}
}

// TestAFailingTransitionIsRetriedAfterItsBackoffAndDelaysNobodyElse pins the
// per-entity backoff: 1s, then 2s, reported each time, while another entity
// due at the same instant is not held back.
func TestAFailingTransitionIsRetriedAfterItsBackoffAndDelaysNobodyElse(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	var failures atomic.Int32
	failures.Store(2)
	def := lifecycle().OnEnter(Expired, func(_ context.Context, i *Item) error {
		if i.ID == "bad" && failures.Add(-1) >= 0 {
			return errBoom
		}
		return nil
	})
	store := newMemStore()
	var r reports
	m := open(t, def, &svcstm.Config[Item, State]{Store: store, Clock: clk, Report: r.add})
	goLive(t, m, Item{ID: "bad", Stock: 1})
	goLive(t, m, Item{ID: "good", Stock: 1})
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	clk.Set(start.Add(time.Hour))
	next, err := m.Step(t.Context())
	if !errors.Is(err, errBoom) || store.read(t, "good").State != Expired || store.read(t, "bad").State != Live {
		t.Fatalf("first pass: %v; good %s, bad %s", err, store.read(t, "good").State, store.read(t, "bad").State)
	}
	if want := start.Add(time.Hour + time.Second); !next.Equal(clk.Now()) && !next.Equal(want) {
		t.Errorf("next after the first failure = %v", next)
	}
	steps := []time.Duration{time.Second, 2 * time.Second}
	for i, wait := range steps {
		// Just before the backoff ends, nothing is tried.
		clk.Advance(wait - time.Millisecond)
		if _, err := m.Step(t.Context()); err != nil {
			t.Fatalf("a pass before the backoff ended = %v", err)
		}
		if got := len(r.all()); got != i+1 {
			t.Fatalf("retry %d came before its backoff: %d failures reported", i+1, got)
		}
		clk.Advance(time.Millisecond)
		_, err := m.Step(t.Context())
		if last := i == len(steps)-1; last != (err == nil) {
			t.Fatalf("retry %d = %v", i+1, err)
		}
	}
	if got := store.read(t, "bad"); got.State != Expired {
		t.Fatalf("after its backoff the failing entity is %s", got.State)
	}
	if reported := r.all(); len(reported) != 2 || !errs.HasCode(reported[0], svcstm.CodeHookFailed) {
		t.Errorf("reported %v", reported)
	}
}

// TestAStateChangedBehindTheMachinesBackReentersItNow pins what a write that
// changes the state tells the machine: the record takes the new state as of
// the moment it learned of it, the census follows, and a delay counts from
// then.
func TestAStateChangedBehindTheMachinesBackReentersItNow(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store := newMemStore()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk})
	wire(t, store, m)
	if _, err := m.Start(t.Context(), Item{ID: "a", Stock: 1}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Minute)
	update(t, store, "a", func(i *Item) { i.State = Live })
	rec, _ := m.Record("a")
	if rec.State != Live || !rec.Entered.Equal(start.Add(10*time.Minute)) || len(rec.History) != 1 {
		t.Fatalf("Record() = %+v; want live since the write, and no step", rec)
	}
	if census := m.Census(); census[Live] != 1 || census[Draft] != 0 {
		t.Errorf("Census() = %v", census)
	}
	next, err := m.Step(t.Context())
	if err != nil || !next.Equal(start.Add(10*time.Minute+time.Hour)) {
		t.Fatalf("Step() = %v, %v; want the delay counted from the write", next, err)
	}
}

// TestTheJournalKeepsTimersAcrossARestart pins the reason a journal exists:
// a machine opened again over the same store and journal counts a delay from
// when the entity really entered its state, drops the records of entities
// that are gone, and — without a journal — every timer restarts.
func TestTheJournalKeepsTimersAcrossARestart(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store, journal := newMemStore(), newMemJournal()
	first := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Journal: journal, Clock: clk})
	goLive(t, first, Item{ID: "a", Stock: 1})
	if err := journal.Save(t.Context(), corestm.RecordValue[State]{Key: "ghost", State: Live, Entered: start}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(30 * time.Minute)

	second := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Journal: journal, Clock: clk})
	next, err := second.Step(t.Context())
	if err != nil || !next.Equal(start.Add(time.Hour)) {
		t.Fatalf("after a restart with a journal, next = %v, %v; want an hour after the publication", next, err)
	}
	if rec, _ := second.Record("a"); len(rec.History) != 2 || rec.History[1].Event != "publish" {
		t.Errorf("the history did not survive the restart: %+v", rec)
	}
	if _, ok := journal.record("ghost"); ok {
		t.Error("the record of an entity no longer in the store was kept")
	}

	third := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk})
	next, err = third.Step(t.Context())
	if err != nil || !next.Equal(clk.Now().Add(time.Hour)) {
		t.Fatalf("after a restart without a journal, next = %v, %v; want an hour from now", next, err)
	}
}

// TestAJournalFailureIsLoudWhereItMatters pins the two sides: a journal that
// cannot be loaded refuses the opening, one that cannot save is reported and
// the transition stands.
func TestAJournalFailureIsLoudWhereItMatters(t *testing.T) {
	t.Parallel()
	journal := newMemJournal()
	journal.failLoad = errBoom
	if _, err := svcstm.NewStateMachine(t.Context(), lifecycle(), &svcstm.Config[Item, State]{Store: newMemStore(), Journal: journal}); !errs.HasCode(err, svcstm.CodeJournalFailed) {
		t.Fatalf("NewStateMachine over an unreadable journal = %v", err)
	}
	journal.failLoad, journal.failSave = nil, errBoom
	var r reports
	store := newMemStore()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Journal: journal, Report: r.add})
	if _, err := m.Start(t.Context(), Item{ID: "a"}); err != nil {
		t.Fatalf("Start() = %v; a journal failure must not fail the transition", err)
	}
	if reported := r.all(); len(reported) != 1 || !errs.HasCode(reported[0], svcstm.CodeJournalFailed) || !errors.Is(reported[0], errBoom) {
		t.Errorf("reported %v", reported)
	}
}

// TestRunAndStepAreOneAtATime pins LoopRunning and the clean stop. It
// launches Run on a background goroutine and cancels its context to end it.
func TestRunAndStepAreOneAtATime(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	onLoop, events := loopEvents()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: newMemStore(), Clock: clk, OnLoop: onLoop})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	awaitRun(t, clk, events, "the first run", func(e svcstm.LoopEvent) bool { return e.Wake == svcstm.WakeStart })
	if _, err := m.Step(t.Context()); !errs.HasCode(err, svcstm.CodeLoopRunning) {
		t.Errorf("Step during Run = %v", err)
	}
	if err := m.Run(t.Context()); !errs.HasCode(err, svcstm.CodeLoopRunning) {
		t.Errorf("a second Run = %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run() = %v after cancellation", err)
	}
	if _, err := m.Step(t.Context()); err != nil {
		t.Errorf("Step after Run returned = %v", err)
	}
}

// observed is what the suite's Observe hook records.
type observed struct {
	firings  []svcstm.FiringValue[State]
	outcomes []error
	mu       sync.Mutex
}

// spanKey is the context value Observe puts in, as a tracer would.
type spanKey struct{}

// TestObserveBracketsWhatTheLoopFires pins the bracket: called for each
// transition the loop fires, with its context reaching the hooks and the
// store, its end told the outcome — and never for a caller's Fire.
func TestObserveBracketsWhatTheLoopFires(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	var o observed
	sawSpan := make(chan bool, 4)
	def := lifecycle().OnEnter(Expired, func(ctx context.Context, _ *Item) error {
		sawSpan <- ctx.Value(spanKey{}) == "span"
		return nil
	})
	m := open(t, def, &svcstm.Config[Item, State]{
		Store: newMemStore(), Clock: clk,
		Observe: func(ctx context.Context, f svcstm.FiringValue[State]) (context.Context, func(error)) {
			o.mu.Lock()
			o.firings = append(o.firings, f)
			o.mu.Unlock()
			return context.WithValue(ctx, spanKey{}, "span"), func(err error) {
				o.mu.Lock()
				o.outcomes = append(o.outcomes, err)
				o.mu.Unlock()
			}
		},
	})
	goLive(t, m, Item{ID: "a", Stock: 1})
	clk.Advance(time.Hour)
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !<-sawSpan {
		t.Error("the hook did not run under the observer's context")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	want := svcstm.FiringValue[State]{Key: "a", Event: "expire", From: Live, To: Expired, Trigger: corestm.TriggerDelay}
	if len(o.firings) != 1 || o.firings[0] != want || len(o.outcomes) != 1 || o.outcomes[0] != nil {
		t.Errorf("observed %+v with outcomes %v; want only the loop's expire", o.firings, o.outcomes)
	}
}

// TestAPanickingGuardIsRetriedAndReported pins FunctionPanicked: the loop
// survives, reports, and tries again after the backoff.
func TestAPanickingGuardIsRetriedAndReported(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	var panics atomic.Int32
	panics.Store(1)
	def := svcstm.NewMachineSpec(stateOf).Initial(Draft).When("ready", Draft, Live, func(i Item) bool {
		if panics.Add(-1) >= 0 {
			panic("a guard's bug")
		}
		return i.Stock > 0
	})
	store := newMemStore()
	var r reports
	m := open(t, def, &svcstm.Config[Item, State]{Store: store, Clock: clk, Report: r.add})
	if _, err := m.Start(t.Context(), Item{ID: "a", Stock: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Step(t.Context()); !errs.HasCode(err, svcstm.CodeFunctionPanicked) {
		t.Fatalf("Step() = %v; want FUNCTION_PANICKED", err)
	}
	clk.Advance(time.Second)
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := store.read(t, "a"); got.State != Live {
		t.Errorf("after the backoff the guard did not fire: %s", got.State)
	}
	if len(r.all()) != 1 {
		t.Errorf("reported %v", r.all())
	}
}

// TestAnEntityGoneWithoutNoticeIsForgottenNotFailed pins what the loop does
// with an entity the store dropped without telling the machine.
func TestAnEntityGoneWithoutNoticeIsForgottenNotFailed(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store := newMemStore()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk})
	goLive(t, m, Item{ID: "a", Stock: 1})
	store.remove(t.Context(), "a")
	clk.Advance(time.Hour)
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatalf("Step() = %v; a vanished entity is not a failure", err)
	}
	if _, ok := m.Record("a"); ok || m.Census()[Live] != 0 {
		t.Error("the vanished entity is still recorded")
	}
}

// TestAWriteThatBringsADeadlineForwardWakesTheLoop pins the wake on a write:
// the loop sleeps until a far deadline, a write moves it closer, and the loop
// re-arms for the new one.
func TestAWriteThatBringsADeadlineForwardWakesTheLoop(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store := newMemStore()
	onLoop, events := loopEvents()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk, OnLoop: onLoop})
	wire(t, store, m)
	running(t, m)
	far, near := start.Add(50*time.Minute), start.Add(20*time.Minute)
	goLive(t, m, Item{ID: "a", Stock: 1, Expires: &far})
	awaitRun(t, clk, events, "the loop asleep until the far deadline", func(e svcstm.LoopEvent) bool { return e.Next.Equal(far) })
	update(t, store, "a", func(i *Item) { i.Expires = &near })
	awaitRun(t, clk, events, "the loop re-armed for the near deadline", func(e svcstm.LoopEvent) bool {
		return e.Wake == svcstm.WakeChange && e.Next.Equal(near)
	})
	clk.Set(near)
	eventually(t, nil, "the near deadline", func() bool { return store.read(t, "a").State == Expired })
}

// TestABurstOfWritesIsOneRun pins the pace: writes arriving before the floor
// — MinGap after the last run — re-arm the loop for the floor, told as a
// Waiting event, and are served by a single run.
func TestABurstOfWritesIsOneRun(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	store := newMemStore()
	onLoop, events := loopEvents()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store, Clock: clk, OnLoop: onLoop, MinGap: 10 * time.Minute})
	wire(t, store, m)
	goLive(t, m, Item{ID: "a", Stock: 5})
	running(t, m)
	awaitRun(t, clk, events, "the settled loop", func(e svcstm.LoopEvent) bool { return e.Next.Equal(start.Add(time.Hour)) })
	drain(events)
	for n := range 10 {
		update(t, store, "a", func(i *Item) { i.Stock = 10 + n })
	}
	waiting := awaitEvent(t, events, svcstm.LoopWaiting)
	if waiting.Next.IsZero() {
		t.Fatalf("the Waiting event = %+v", waiting)
	}
	clk.Set(waiting.Next)
	awaitRun(t, clk, events, "the one run the burst makes", func(e svcstm.LoopEvent) bool { return e.Wake == svcstm.WakeChange })
	time.Sleep(20 * time.Millisecond)
	if n := count(events, svcstm.LoopRunStarted); n != 0 {
		t.Errorf("the burst of ten writes made %d more runs", n)
	}
}

// awaitEvent returns the next event of kind.
func awaitEvent(t *testing.T, events chan svcstm.LoopEvent, kind svcstm.LoopEventKind) svcstm.LoopEvent {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("the loop events were closed")
			}
			if e.Kind == kind {
				return e
			}
		case <-deadline:
			t.Fatalf("no %s event", kind)
		}
	}
}

// drain empties events.
func drain(events chan svcstm.LoopEvent) {
	count(events, 0)
}

// count drains events and counts those of kind.
func count(events chan svcstm.LoopEvent, kind svcstm.LoopEventKind) int {
	n := 0
	for {
		select {
		case e, ok := <-events:
			if !ok {
				return n
			}
			if e.Kind == kind {
				n++
			}
		default:
			return n
		}
	}
}
