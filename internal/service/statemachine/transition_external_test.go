// Package statemachine_test — transitions a caller asks for: Start and Fire,
// the hooks on the way, what the machine records, and the refusals.
package statemachine_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcstm "github.com/kitsunium/sdk/internal/service/statemachine"
)

// actorKey carries who calls, the way a framework's context does.
type actorKey struct{}

// TestEventsMoveAnEntityAndTheMachineRecordsEachStep pins the event path: a
// creation, a refusal from the wrong state with the entity returned, the
// OnEnter hook changing the entity, the OnTransition hooks seeing each change,
// the history with its actor, and a census that lists every declared state.
func TestEventsMoveAnEntityAndTheMachineRecordsEachStep(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(start)
	var mu sync.Mutex
	var changes []svcstm.ChangeValue[Item, State]
	def := lifecycle().
		OnEnter(Sold, func(_ context.Context, i *Item) error { i.Note = "sold"; return nil }).
		OnTransition(func(_ context.Context, c svcstm.ChangeValue[Item, State]) error {
			mu.Lock()
			changes = append(changes, c)
			mu.Unlock()
			return nil
		})
	store := newMemStore()
	m := open(t, def, &svcstm.Config[Item, State]{Store: store, Clock: clk, Actor: func(ctx context.Context) string {
		who, _ := ctx.Value(actorKey{}).(string)
		return who
	}})
	ctx := context.WithValue(t.Context(), actorKey{}, "shop/endpoint/Fire")

	created, err := m.Start(ctx, Item{ID: "chair", Stock: 5})
	if err != nil || created.State != Draft {
		t.Fatalf("Start() = %+v, %v", created, err)
	}
	refused, err := m.Fire(ctx, "chair", "sell")
	if !errs.HasCode(err, svcstm.CodeTransitionRefused) || refused.State != Draft || errs.HTTPStatusOf(err) != 409 {
		t.Fatalf("selling a draft = %+v, %v; want TRANSITION_REFUSED with the draft", refused, err)
	}
	for _, event := range []string{"publish", "sell"} {
		clk.Advance(time.Minute)
		if _, err := m.Fire(ctx, "chair", event); err != nil {
			t.Fatalf("Fire(%s) = %v", event, err)
		}
	}
	if got := store.read(t, "chair"); got.State != Sold || got.Note != "sold" {
		t.Fatalf("stored %+v: the OnEnter hook's change was not kept", got)
	}
	census := m.Census()
	if census[Sold] != 1 || census[Draft] != 0 || len(census) != 5 {
		t.Errorf("Census() = %v: every declared state must be present", census)
	}
	rec, ok := m.Record("chair")
	if !ok || rec.State != Sold || !rec.Entered.Equal(start.Add(2*time.Minute)) || len(rec.History) != 3 {
		t.Fatalf("Record() = %+v, %v", rec, ok)
	}
	first := rec.History[0]
	if first.Event != corestm.CreateEvent || first.Trigger != corestm.TriggerStart || first.From != 0 || first.To != Draft || first.Actor != "shop/endpoint/Fire" {
		t.Errorf("the creation step = %+v", first)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(changes) != 3 || changes[2].Event != "sell" || changes[2].From != Live || changes[2].Entity.Note != "sold" || changes[2].Actor != "shop/endpoint/Fire" {
		t.Errorf("the OnTransition hooks saw %+v", changes)
	}
}

// TestFireOnAKeyTheStoreDoesNotHoldIsEntityMissing pins the 404.
func TestFireOnAKeyTheStoreDoesNotHoldIsEntityMissing(t *testing.T) {
	t.Parallel()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: newMemStore()})
	_, err := m.Fire(t.Context(), "nobody", "publish")
	if !errs.HasCode(err, svcstm.CodeEntityMissing) || errs.HTTPStatusOf(err) != 404 {
		t.Fatalf("Fire(nobody) = %v; want ENTITY_MISSING and 404", err)
	}
}

// TestStartRefusesATakenKeyAndAnEmptyOne pins the two creation refusals and
// that neither stores anything nor leaves a record.
func TestStartRefusesATakenKeyAndAnEmptyOne(t *testing.T) {
	t.Parallel()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: newMemStore()})
	if _, err := m.Start(t.Context(), Item{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(t.Context(), Item{ID: "a"}); !errs.HasCode(err, svcstm.CodeEntityExists) || errs.HTTPStatusOf(err) != 409 {
		t.Errorf("a second Start of one key = %v", err)
	}
	if _, err := m.Start(t.Context(), Item{}); !errs.HasCode(err, svcstm.CodeKeyEmpty) {
		t.Errorf("Start with no key = %v", err)
	}
	if census := m.Census(); census[Draft] != 1 {
		t.Errorf("Census() = %v after one creation and two refusals", census)
	}
}

// TestAPanickingOnEnterHookFailsItsTransitionAndFreesTheEntity is the defect
// kit fixed with CodeWorkflowHookPanic: a hook runs under the lock, and a
// panic that escaped it left every later transition of the machine blocked.
func TestAPanickingOnEnterHookFailsItsTransitionAndFreesTheEntity(t *testing.T) {
	t.Parallel()
	def := svcstm.NewMachineSpec(stateOf).Initial(Draft).On("do", Draft, Live).On("undo", Live, Draft).
		OnEnter(Live, func(_ context.Context, i *Item) error {
			if i.Note == "boom" {
				panic("an OnEnter hook's bug")
			}
			return nil
		})
	store := newMemStore()
	m := open(t, def, &svcstm.Config[Item, State]{Store: store})
	for _, i := range []Item{{ID: "boom", Note: "boom"}, {ID: "fine"}} {
		if _, err := m.Start(t.Context(), i); err != nil {
			t.Fatal(err)
		}
	}
	_, err := fireWithin(t, m, "boom", "do")
	if !errs.HasCode(err, svcstm.CodeHookPanicked) {
		t.Fatalf("the panicking transition = %v; want HOOK_PANICKED", err)
	}
	if got := store.read(t, "boom"); got.State != Draft {
		t.Errorf("the failed transition was stored: %q", got.State)
	}
	if fields := errs.FieldsOf(err); !slices.ContainsFunc(fields, func(f errs.FieldValue) bool { return f.Key() == "stack" && f.StringValue() != "" }) {
		t.Errorf("the panic's stack is not among the fields: %v", fields)
	}
	// The same entity, and another one: neither is locked.
	store.put(t, Item{ID: "boom", State: Draft})
	if got, err := fireWithin(t, m, "boom", "do"); err != nil || got.State != Live {
		t.Fatalf("the same entity after the panic = %+v, %v", got, err)
	}
	if got, err := fireWithin(t, m, "fine", "do"); err != nil || got.State != Live {
		t.Fatalf("another entity after the panic = %+v, %v", got, err)
	}
}

// TestAPanickingOnTransitionHookLeavesTheTransitionStanding pins the other
// hook: after the store, so the transition stands, the hooks after it still
// run, and the panic goes to Report.
func TestAPanickingOnTransitionHookLeavesTheTransitionStanding(t *testing.T) {
	t.Parallel()
	ran := make(chan string, 4)
	def := svcstm.NewMachineSpec(stateOf).Initial(Draft).On("do", Draft, Live).
		OnTransition(func(_ context.Context, c svcstm.ChangeValue[Item, State]) error {
			if c.Event == "do" {
				panic("an OnTransition hook's bug")
			}
			return nil
		}).
		OnTransition(func(_ context.Context, c svcstm.ChangeValue[Item, State]) error {
			ran <- c.Event
			return nil
		})
	var r reports
	m := open(t, def, &svcstm.Config[Item, State]{Store: newMemStore(), Report: r.add})
	if _, err := m.Start(t.Context(), Item{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	<-ran
	got, err := fireWithin(t, m, "a", "do")
	if err != nil || got.State != Live {
		t.Fatalf("Fire() = %+v, %v; want the transition standing", got, err)
	}
	if event := <-ran; event != "do" {
		t.Errorf("the hook after the panicking one saw %q", event)
	}
	if reported := r.all(); len(reported) != 1 || !errs.HasCode(reported[0], svcstm.CodeHookPanicked) {
		t.Errorf("Report received %v", reported)
	}
}

// TestAHookErrorKeepsItsOwnIdentity pins the join: the caller's own error is
// reachable with errors.Is, the verdict with errs.HasCode.
func TestAHookErrorKeepsItsOwnIdentity(t *testing.T) {
	t.Parallel()
	def := svcstm.NewMachineSpec(stateOf).Initial(Draft).On("do", Draft, Live).
		OnEnter(Live, func(context.Context, *Item) error { return errBoom })
	store := newMemStore()
	m := open(t, def, &svcstm.Config[Item, State]{Store: store})
	if _, err := m.Start(t.Context(), Item{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	got, err := m.Fire(t.Context(), "a", "do")
	if !errors.Is(err, errBoom) || !errs.HasCode(err, svcstm.CodeHookFailed) || got.State != Draft {
		t.Fatalf("Fire() = %+v, %v", got, err)
	}
	if rec, _ := m.Record("a"); rec.State != Draft || len(rec.History) != 1 {
		t.Errorf("a refused transition was recorded: %+v", rec)
	}
}

// TestAHookMayChangeTheEntityButNotItsStateNorItsKey pins the two checks made
// after the OnEnter hooks.
func TestAHookMayChangeTheEntityButNotItsStateNorItsKey(t *testing.T) {
	t.Parallel()
	def := svcstm.NewMachineSpec(stateOf).Initial(Draft).On("state", Draft, Live).On("key", Draft, Sold).
		OnEnter(Live, func(_ context.Context, i *Item) error { i.State = Expired; return nil }).
		OnEnter(Sold, func(_ context.Context, i *Item) error { i.ID = "other"; return nil })
	store := newMemStore()
	m := open(t, def, &svcstm.Config[Item, State]{Store: store})
	if _, err := m.Start(t.Context(), Item{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Fire(t.Context(), "a", "state"); !errs.HasCode(err, svcstm.CodeHookChangedState) {
		t.Errorf("a hook changing the state = %v", err)
	}
	if _, err := m.Fire(t.Context(), "a", "key"); !errs.HasCode(err, svcstm.CodeHookChangedKey) {
		t.Errorf("a hook changing the key = %v", err)
	}
	if _, ok, err := store.Get(t.Context(), "other"); err != nil || ok || store.read(t, "a").State != Draft {
		t.Errorf("a refused hook's entity was stored: %v", err)
	}
}

// TestAnOnEnterHookFiringItsOwnMachineIsRefusedNotDeadlocked pins Reentrant,
// and that an OnTransition hook, which runs after the lock is released, may
// fire the machine again.
func TestAnOnEnterHookFiringItsOwnMachineIsRefusedNotDeadlocked(t *testing.T) {
	t.Parallel()
	var m *svcstm.StateMachine[Item, State]
	inner := make(chan error, 1)
	def := svcstm.NewMachineSpec(stateOf).Initial(Draft).On("do", Draft, Live).On("next", Live, Sold).
		OnEnter(Live, func(ctx context.Context, i *Item) error {
			_, err := m.Fire(ctx, "other", "do")
			inner <- err
			return nil
		}).
		OnTransition(func(ctx context.Context, c svcstm.ChangeValue[Item, State]) error {
			if c.Event == "do" {
				_, err := m.Fire(ctx, c.Key, "next")
				return err
			}
			return nil
		})
	store := newMemStore()
	var r reports
	m = open(t, def, &svcstm.Config[Item, State]{Store: store, Report: r.add})
	for _, key := range []string{"a", "other"} {
		if _, err := m.Start(t.Context(), Item{ID: key}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fireWithin(t, m, "a", "do"); err != nil {
		t.Fatal(err)
	}
	if err := <-inner; !errs.HasCode(err, svcstm.CodeReentrant) {
		t.Errorf("an OnEnter hook firing its machine = %v; want REENTRANT", err)
	}
	if got := store.read(t, "a"); got.State != Sold {
		t.Errorf("the OnTransition hook's Fire did not move the entity: %q", got.State)
	}
	if len(r.all()) != 0 {
		t.Errorf("reported %v", r.all())
	}
}

// TestATransitionNeverResurrects pins the replace-only write: an OnEnter hook
// deletes its own entity — through the store, which tells the machine — and
// the transition fails rather than write it back, without deadlocking, and
// the machine keeps no record of it.
func TestATransitionNeverResurrects(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	def := lifecycle().OnEnter(Retired, func(ctx context.Context, i *Item) error {
		store.remove(ctx, i.ID)
		return nil
	})
	j := newMemJournal()
	m := open(t, def, &svcstm.Config[Item, State]{Store: store, Journal: j})
	wire(t, store, m)
	if _, err := m.Start(t.Context(), Item{ID: "doomed"}); err != nil {
		t.Fatal(err)
	}
	_, err := fireWithin(t, m, "doomed", "retire")
	if !errs.HasCode(err, svcstm.CodeEntityMissing) {
		t.Fatalf("retire = %v; want ENTITY_MISSING", err)
	}
	if _, ok, err := store.Get(t.Context(), "doomed"); err != nil || ok {
		t.Fatalf("the deleted entity was written back: %v", err)
	}
	if _, ok := m.Record("doomed"); ok {
		t.Error("the machine still records the deleted entity")
	}
	if _, ok := j.record("doomed"); ok {
		t.Error("the journal still holds the deleted entity")
	}
	if census := m.Census(); census[Draft] != 0 || census[Retired] != 0 {
		t.Errorf("Census() = %v", census)
	}
}

// TestADeleteDuringATransitionIsNotUndoneByItsRecord pins the flight: an
// entity deleted after its transition stored it, but before the step is
// recorded, keeps no record.
func TestADeleteDuringATransitionIsNotUndoneByItsRecord(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	def := svcstm.NewMachineSpec(stateOf).Initial(Draft).On("do", Draft, Live)
	m := open(t, def, &svcstm.Config[Item, State]{Store: store})
	if _, err := m.Start(t.Context(), Item{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	// From now on the store deletes an entity the moment it is written — so
	// inside the transition, after its write and before its step — and says
	// so.
	store.mu.Lock()
	store.notify = func(ctx context.Context, key string, deleted bool) {
		if !deleted && store.remove(ctx, key) {
			if err := m.Deleted(ctx, key); err != nil {
				t.Errorf("Deleted() = %v", err)
			}
		}
	}
	store.mu.Unlock()
	if _, err := m.Fire(t.Context(), "a", "do"); err != nil {
		t.Fatalf("Fire() = %v: the write itself succeeded", err)
	}
	if _, ok := m.Record("a"); ok {
		t.Error("the transition's record brought back an entity deleted during it")
	}
	if census := m.Census(); census[Live] != 0 || census[Draft] != 0 {
		t.Errorf("Census() = %v", census)
	}
}

// TestAStoreFailureIsTheStoresAndNothingIsRecorded pins StoreFailed on each
// operation a transition makes.
func TestAStoreFailureIsTheStoresAndNothingIsRecorded(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: store})
	store.failWith(errBoom)
	if _, err := m.Start(t.Context(), Item{ID: "a"}); !errs.HasCode(err, svcstm.CodeStoreFailed) || !errors.Is(err, errBoom) {
		t.Errorf("a failed insert = %v", err)
	}
	if _, err := m.Start(t.Context(), Item{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	store.failWith(errBoom)
	if _, err := m.Fire(t.Context(), "a", "publish"); !errs.HasCode(err, svcstm.CodeStoreFailed) {
		t.Errorf("a failed read = %v", err)
	}
	if rec, _ := m.Record("a"); rec.State != Draft || len(rec.History) != 1 {
		t.Errorf("a failed transition was recorded: %+v", rec)
	}
}

// TestAWaitForABusyEntityEndsWithItsContext pins WaitAbandoned: a caller whose
// context ends while another transition holds the entity stops waiting. The
// slow transition runs on a background goroutine the test releases and joins.
func TestAWaitForABusyEntityEndsWithItsContext(t *testing.T) {
	t.Parallel()
	entered, leave := make(chan struct{}), make(chan struct{})
	def := svcstm.NewMachineSpec(stateOf).Initial(Draft).On("slow", Draft, Live).On("fast", Draft, Sold).
		OnEnter(Live, func(context.Context, *Item) error {
			close(entered)
			<-leave
			return nil
		})
	m := open(t, def, &svcstm.Config[Item, State]{Store: newMemStore()})
	if _, err := m.Start(t.Context(), Item{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	slow := make(chan error, 1)
	go func() {
		_, err := m.Fire(context.Background(), "a", "slow")
		slow <- err
	}()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := m.Fire(ctx, "a", "fast")
	if !errs.HasCode(err, svcstm.CodeWaitAbandoned) || !errors.Is(err, context.Canceled) || errs.HTTPStatusOf(err) != 503 {
		t.Errorf("Fire with an ended context = %v", err)
	}
	close(leave)
	if err := <-slow; err != nil {
		t.Fatal(err)
	}
}

// TestTransitionsOfManyEntitiesRunConcurrently drives many entities from many
// concurrent goroutines, some of them racing on one entity, joins them all,
// and checks the census adds up: the race detector watches the rest.
func TestTransitionsOfManyEntitiesRunConcurrently(t *testing.T) {
	t.Parallel()
	const entities, racers int = 32, 4
	m := open(t, lifecycle(), &svcstm.Config[Item, State]{Store: newMemStore()})
	var wg sync.WaitGroup
	for i := range entities {
		wg.Go(func() {
			key := string(rune('A' + i))
			if _, err := m.Start(context.Background(), Item{ID: key, Stock: 1}); err != nil {
				t.Error(err)
				return
			}
			var inner sync.WaitGroup
			for range racers {
				inner.Go(func() {
					if _, err := m.Fire(context.Background(), key, "publish"); err != nil && !errs.HasCode(err, svcstm.CodeTransitionRefused) {
						t.Error(err)
					}
				})
			}
			inner.Wait()
		})
	}
	wg.Wait()
	if census := m.Census(); census[Live] != entities || census[Draft] != 0 {
		t.Fatalf("Census() = %v; want every entity live once", census)
	}
	for _, rec := range m.Records() {
		if len(rec.History) != 2 {
			t.Errorf("%s has %d steps: a racing Fire moved it twice", rec.Key, len(rec.History))
		}
	}
}

// fireWithin fires event on key from a background goroutine, failing the
// test when the machine does not answer in time — a lock left held would block
// it forever, and the goroutine with it.
func fireWithin(t *testing.T, m *svcstm.StateMachine[Item, State], key, event string) (Item, error) {
	t.Helper()
	type result struct {
		err  error
		item Item
	}
	done := make(chan result, 1)
	go func() {
		i, err := m.Fire(context.Background(), key, event)
		done <- result{item: i, err: err}
	}()
	select {
	case r := <-done:
		return r.item, r.err
	case <-time.After(10 * time.Second):
		t.Fatalf("Fire(%q, %q) never returned: the entity is still locked", key, event)
		return Item{}, nil
	}
}
