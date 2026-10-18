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

// setReady makes an entity ready.
func setReady(g *gated) error { g.Ready = true; return nil }

// The engine's loop reads an entity to check its guard outside any
// transaction, and in memory a transaction's write lands before its commit:
// the loop could fire on a write a rollback then undid, and its transition,
// which waits for the writer turn, wrote the entity back afterwards, the
// rolled back write with it — what made the storetest case
// hooks-after-the-commit fail under load. The interleaving is played here
// step by step: the replacement is refused, and the store keeps what the
// rollback left.
func TestALoopTransitionNeverWritesBackARolledBackWrite(t *testing.T) {
	items, port, _ := gatedFlow(t)
	errRolled := errors.New("rolled back")
	var read gated
	err := Transact(t.Context(), func(ctx context.Context) error {
		if _, err := items.Update(ctx, "g", setReady); err != nil {
			return err
		}
		read = loopRead(t, port, "g")
		return errRolled
	})
	if !errors.Is(err, errRolled) {
		t.Fatal(err)
	}
	if !read.Ready {
		t.Fatal("setup: the loop's read did not see the transaction's write")
	}
	err = fireOn(t.Context(), port, read, func(context.Context) error { return nil })
	if ke, ok := errors.AsType[*Error](err); !ok || ke.Code != WireUnavailable {
		t.Fatalf("a transition decided on a rolled back write: %v, want it refused as unavailable", err)
	}
	got, err := items.Get(t.Context(), "g")
	if err != nil || got.Ready || got.State != "new" {
		t.Fatalf("after the refused transition the store holds %+v %v, want the entity as the rollback left it", got, err)
	}
	// The loop's next read is of what the store holds, and nothing holds
	// its transition back.
	read = loopRead(t, port, "g")
	read.Ready = true
	if err := fireOn(t.Context(), port, read, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("a transition decided on a committed read: %v", err)
	}
}

// What counts is a rollback of another transaction: one the transition's own
// transaction makes — a savepoint of a hook — undoes nothing the loop read.
func TestALoopTransitionStandsOverItsOwnSavepointsRollback(t *testing.T) {
	items, port, _ := gatedFlow(t)
	must(t, Transact(t.Context(), func(ctx context.Context) error {
		_, err := items.Update(ctx, "g", setReady)
		return err
	}))
	read := loopRead(t, port, "g")
	errRolled := errors.New("rolled back")
	err := fireOn(t.Context(), port, read, func(ctx context.Context) error {
		inner := Transact(ctx, func(ctx context.Context) error {
			return errors.Join(items.Insert(ctx, gated{ID: "other"}), errRolled)
		})
		if !errors.Is(inner, errRolled) {
			return inner
		}
		return nil
	})
	if err != nil {
		t.Fatalf("a transition whose own savepoint rolled back: %v", err)
	}
	if got, err := items.Get(t.Context(), "g"); err != nil || got.State != "done" {
		t.Fatalf("the store holds %+v %v, want the transition stored", got, err)
	}
}

// A rollback is compared for the entity read, not for its whole store: one
// that wrote h back leaves the transition the loop decided on g standing,
// and one that wrote g back still refuses it.
func TestALoopTransitionStandsOverAnotherEntitysRollback(t *testing.T) {
	items, port, _ := gatedFlow(t)
	errRolled := errors.New("rolled back")
	rollBack := func(key string) {
		t.Helper()
		err := Transact(t.Context(), func(ctx context.Context) error {
			_, err := items.Update(ctx, key, setReady)
			return errors.Join(err, errRolled)
		})
		if !errors.Is(err, errRolled) {
			t.Fatal(err)
		}
	}
	read := loopRead(t, port, "g")
	rollBack("h")
	if err := fireOn(t.Context(), port, read, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("a transition on g after a rollback of h: %v", err)
	}
	read = loopRead(t, port, "h")
	rollBack("h")
	err := fireOn(t.Context(), port, read, func(context.Context) error { return nil })
	if ke, ok := errors.AsType[*Error](err); !ok || ke.Code != WireUnavailable {
		t.Fatalf("a transition on h after a rollback of h: %v, want it refused as unavailable", err)
	}
}

// What the loop read is forgotten when its entity is deleted, when the read
// found nothing, and when the workflow stops: it holds no more than the
// entities the store holds.
func TestALoopsReadsAreForgotten(t *testing.T) {
	items, port, stop := gatedFlow(t)
	loopRead(t, port, "g")
	if _, ok := remembered(port, "g"); !ok {
		t.Fatal("setup: the loop's read of g is not remembered")
	}
	must(t, items.Delete(t.Context(), "g"))
	if _, ok := remembered(port, "g"); ok {
		t.Fatal("the read of g is still remembered after its deletion")
	}
	if _, found, err := port.Get(context.Background(), "g"); found || err != nil {
		t.Fatalf("a read of a deleted entity: %v %v", found, err)
	}
	if _, ok := remembered(port, "g"); ok {
		t.Fatal("a read of g that found nothing is remembered")
	}
	loopRead(t, port, "h")
	stop()
	if n, _ := remembered(port, ""); n != 0 {
		t.Fatalf("after the stop %d reads are remembered, want 0", n)
	}
}
