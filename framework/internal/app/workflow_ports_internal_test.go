package app

import (
	"context"
	"errors"
	"io"
	"testing"
)

// gated is the entity of a workflow whose guard reads Ready.
type gated struct {
	ID    string `json:"id"`
	Ready bool   `json:"ready,omitempty"`
	State string `json:"state,omitempty"`
}

// gatedFlow runs, in memory, a workflow over gated entities from new to done,
// and holds the entity g in new. The test plays its loop through the port: the
// workflow declares no guard, so its own loop never runs and cannot read in
// between.
func gatedFlow(t *testing.T) (*StoreService[gated], workflowPort[gated, string]) {
	t.Helper()
	svc := NewService("gates", "")
	items := svc.Store("items", func(g gated) string { return g.ID })
	flow := svc.Workflow("gate", items, func(g *gated) *string { return &g.State }).
		Initial("new").On("ready", "new", "done")
	a := NewApp("gates", svc).With(Profile("daemon"), InMemory(), Logs(io.Discard))
	must(t, a.Start(t.Context()))
	t.Cleanup(func() { must(t, a.Stop(context.Background())) })
	if _, err := flow.Start(t.Context(), gated{ID: "g"}); err != nil {
		t.Fatal(err)
	}
	return items, workflowPort[gated, string]{w: flow}
}

// loopRead reads g through the port as the engine's loop does, outside any
// transaction.
func loopRead(t *testing.T, port workflowPort[gated, string]) gated {
	t.Helper()
	d, found, err := port.Get(context.Background(), "g")
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
	items, port := gatedFlow(t)
	errRolled := errors.New("rolled back")
	var read gated
	err := Transact(t.Context(), func(ctx context.Context) error {
		if _, err := items.Update(ctx, "g", setReady); err != nil {
			return err
		}
		read = loopRead(t, port)
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
	read = loopRead(t, port)
	read.Ready = true
	if err := fireOn(t.Context(), port, read, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("a transition decided on a committed read: %v", err)
	}
}

// What counts is a rollback of another transaction: one the transition's own
// transaction makes — a savepoint of a hook — undoes nothing the loop read.
func TestALoopTransitionStandsOverItsOwnSavepointsRollback(t *testing.T) {
	items, port := gatedFlow(t)
	must(t, Transact(t.Context(), func(ctx context.Context) error {
		_, err := items.Update(ctx, "g", setReady)
		return err
	}))
	read := loopRead(t, port)
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
