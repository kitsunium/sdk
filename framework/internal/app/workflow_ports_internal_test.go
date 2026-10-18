package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
)

// gated is the entity of a workflow whose guard reads Ready.
type gated struct {
	ID    string `json:"id"`
	Ready bool   `json:"ready,omitempty"`
	State string `json:"state,omitempty"`
}

// errRolled is what the tests' transactions that roll back return.
var errRolled = errors.New("rolled back")

// gatedFlow runs, in memory, a workflow over gated entities from new to done,
// and holds the entities g and h in new — two keys of different slots of
// undoneCounts. The test plays its loop through the port: the workflow
// declares no guard, so its own loop never runs and cannot read in between.
// The app is stopped by the function returned, or when the test ends.
func gatedFlow(t *testing.T) (*StoreService[gated], workflowPort[gated, string], func()) {
	t.Helper()
	if slotOf("g") == slotOf("h") {
		t.Fatal("setup: g and h share a slot")
	}
	svc := NewService("gates", "")
	items := svc.Store("items", func(g gated) string { return g.ID })
	flow := svc.Workflow("gate", items, func(g *gated) *string { return &g.State }).
		Initial("new").On("ready", "new", "done")
	a := NewApp("gates", svc).With(Profile("daemon"), InMemory(), Logs(io.Discard))
	must(t, a.Start(t.Context()))
	var once sync.Once
	stop := func() { once.Do(func() { must(t, a.Stop(context.Background())) }) }
	t.Cleanup(stop)
	for _, id := range []string{"g", "h"} {
		if _, err := flow.Start(t.Context(), gated{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	return items, workflowPort[gated, string]{w: flow}, stop
}

// remembered is how many of the loop's reads port's workflow remembers, and
// whether one of key is among them.
func remembered(port workflowPort[gated, string], key string) (int, bool) {
	r := &port.w.reads
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.byKey[key]
	return len(r.byKey), ok
}

// loopRead reads key through the port as the engine's loop does, outside any
// transaction.
func loopRead(t *testing.T, port workflowPort[gated, string], key string) gated {
	t.Helper()
	d, found, err := port.Get(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("the loop's read: %v %v", found, err)
	}
	return as[gated](d.Value)
}

// fireOn stores, in a transaction of its own as the loop's bracket opens, the
// transition to done the loop decided on read; inner runs in it first.
func fireOn(ctx context.Context, port workflowPort[gated, string], read gated, inner func(context.Context) error) error {
	return Transact(ctx, func(ctx context.Context) error {
		if err := inner(ctx); err != nil {
			return err
		}
		read.State = "done"
		_, err := port.Replace(ctx, port.w.entityValue(read))
		return err
	})
}

// nothing is a transition's transaction with nothing before its write.
func nothing(context.Context) error { return nil }

// setReady makes an entity ready.
func setReady(g *gated) error { g.Ready = true; return nil }

// rollBack makes key ready in a transaction that rolls back; read, when not
// nil, runs inside it once the write landed.
func rollBack(t *testing.T, items *StoreService[gated], key string, read func()) {
	t.Helper()
	err := Transact(t.Context(), func(ctx context.Context) error {
		if _, err := items.Update(ctx, key, setReady); err != nil {
			return err
		}
		if read != nil {
			read()
		}
		return errRolled
	})
	if !errors.Is(err, errRolled) {
		t.Fatal(err)
	}
}

// refusedUnavailable reports whether err is the refusal of a transition
// decided on a rolled back write.
func refusedUnavailable(err error) bool {
	ke, ok := errors.AsType[*Error](err)
	return ok && ke.Code == WireUnavailable
}

// The engine's loop reads an entity to check its guard outside any
// transaction, and in memory a transaction's write lands before its commit:
// the loop could fire on a write a rollback then undid, and its transition,
// which waits for the writer turn, wrote the entity back afterwards, the
// rolled back write with it — what made the storetest case
// hooks-after-the-commit fail under load. The interleaving is played here
// step by step: the replacement is refused, and the store keeps what the
// rollback left; the loop's next read is of what the store holds, and nothing
// holds its transition back.
func TestALoopTransitionNeverWritesBackARolledBackWrite(t *testing.T) {
	type tc struct {
		name string
		// reread reads the entity again after the rollback, as the loop's
		// next pass does, before the transition is fired.
		reread bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		items, port, _ := gatedFlow(t)
		var read gated
		rollBack(t, items, "g", func() { read = loopRead(t, port, "g") })
		if !read.Ready {
			t.Fatal("setup: the loop's read did not see the transaction's write")
		}
		if c.reread {
			read = loopRead(t, port, "g")
			read.Ready = true
			if err := fireOn(t.Context(), port, read, nothing); err != nil {
				t.Fatalf("a transition decided on a committed read: %v", err)
			}
			return
		}
		if err := fireOn(t.Context(), port, read, nothing); !refusedUnavailable(err) {
			t.Fatalf("a transition decided on a rolled back write: %v, want it refused as unavailable", err)
		}
		got, err := items.Get(t.Context(), "g")
		if err != nil || got.Ready || got.State != "new" {
			t.Fatalf("after the refused transition the store holds %+v %v, want the entity as the rollback left it", got, err)
		}
	}
	for _, c := range []tc{
		{name: "decided on the rolled back write", reread: false},
		{name: "decided on a read after the rollback", reread: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// What counts is a rollback of another transaction: one the transition's own
// transaction makes — a savepoint of a hook — undoes nothing the loop read.
func TestALoopTransitionStandsOverItsOwnSavepointsRollback(t *testing.T) {
	type tc struct {
		name string
		// savepoint rolls back a savepoint of the transition's transaction
		// before its write.
		savepoint bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		items, port, _ := gatedFlow(t)
		must(t, Transact(t.Context(), func(ctx context.Context) error {
			_, err := items.Update(ctx, "g", setReady)
			return err
		}))
		read := loopRead(t, port, "g")
		inner := nothing
		if c.savepoint {
			inner = func(ctx context.Context) error {
				err := Transact(ctx, func(ctx context.Context) error {
					return errors.Join(items.Insert(ctx, gated{ID: "other"}), errRolled)
				})
				if !errors.Is(err, errRolled) {
					return err
				}
				return nil
			}
		}
		if err := fireOn(t.Context(), port, read, inner); err != nil {
			t.Fatalf("a transition (own savepoint rolled back: %v): %v", c.savepoint, err)
		}
		if got, err := items.Get(t.Context(), "g"); err != nil || got.State != "done" {
			t.Fatalf("the store holds %+v %v, want the transition stored", got, err)
		}
	}
	for _, c := range []tc{
		{name: "its own savepoint rolled back", savepoint: true},
		{name: "no savepoint", savepoint: false},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// A rollback is compared for the entity read, not for its whole store: one
// that wrote h back leaves the transition the loop decided on g standing,
// and one that wrote h back still refuses h's.
func TestALoopTransitionStandsOverAnotherEntitysRollback(t *testing.T) {
	type tc struct {
		name string
		// read is the entity the loop read and transitions, rolled the one
		// a rollback wrote back after the read.
		read, rolled string
		refused      bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		items, port, _ := gatedFlow(t)
		read := loopRead(t, port, c.read)
		rollBack(t, items, c.rolled, nil)
		err := fireOn(t.Context(), port, read, nothing)
		if c.refused && !refusedUnavailable(err) {
			t.Fatalf("a transition on %s after a rollback of %s: %v, want it refused as unavailable", c.read, c.rolled, err)
		}
		if !c.refused && err != nil {
			t.Fatalf("a transition on %s after a rollback of %s: %v", c.read, c.rolled, err)
		}
	}
	for _, c := range []tc{
		{name: "another entity rolled back", read: "g", rolled: "h", refused: false},
		{name: "the entity read rolled back", read: "h", rolled: "h", refused: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// What the loop read is forgotten when its entity is deleted, when the read
// found nothing, and when the workflow stops: it holds no more than the
// entities the store holds.
func TestALoopsReadsAreForgotten(t *testing.T) {
	type tc struct {
		name string
		// key is the entity read; forget is what should make the read
		// forgotten.
		key    string
		forget func(t *testing.T, items *StoreService[gated], port workflowPort[gated, string], stop func())
		// all says that no read at all is left, not only key's.
		all bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		items, port, stop := gatedFlow(t)
		if _, found, err := port.Get(context.Background(), c.key); err != nil {
			t.Fatalf("the loop's read of %s: %v %v", c.key, found, err)
		}
		c.forget(t, items, port, stop)
		n, ok := remembered(port, c.key)
		if ok {
			t.Fatalf("the read of %s is still remembered", c.key)
		}
		if c.all && n != 0 {
			t.Fatalf("%d reads are remembered, want 0", n)
		}
	}
	for _, c := range []tc{
		{name: "the entity deleted", key: "g", forget: func(t *testing.T, items *StoreService[gated], port workflowPort[gated, string], _ func()) {
			t.Helper()
			if _, ok := remembered(port, "g"); !ok {
				t.Fatal("setup: the loop's read of g is not remembered")
			}
			must(t, items.Delete(t.Context(), "g"))
		}},
		{name: "a read that found nothing", key: "absent", forget: func(*testing.T, *StoreService[gated], workflowPort[gated, string], func()) {}},
		{name: "the workflow stopped", key: "h", all: true, forget: func(_ *testing.T, _ *StoreService[gated], _ workflowPort[gated, string], stop func()) {
			stop()
		}},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
