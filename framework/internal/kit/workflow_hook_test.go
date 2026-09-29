package kit_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A service whose workflow hooks misbehave on purpose: a hook is the
// product's code, and the framework must survive it.

var Hooks = kit.NewService("hooks", "A workflow whose hooks panic, for the tests.")

type Chore struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// Boom makes the hooks panic for this chore.
	Boom bool `json:"boom"`
}

var Chores = Hooks.Store("chores", func(c Chore) string { return c.ID })

// ranAfter counts the OnTransition hooks that ran past the one that panics.
var ranAfter = make(chan string, 8)

var ChoreFlow = Hooks.Workflow("chore", Chores, func(c *Chore) *string { return &c.State }).
	Initial("todo").
	On("do", "todo", "done").
	OnEnter("done", panicOnBoom).
	OnTransition(panicAfterBoom).
	OnTransition(noteAfter)

// breakTheRules fires its own workflow: it is declared once the workflow is,
// which the package's variable initialisation orders.
var _ = ChoreFlow.OnEnter("done", breakTheRules)

// breakTheRules breaks, for the chore named after it, a rule an OnEnter hook
// lives by: it may change its entity, not the state it enters nor its key,
// and must not fire its own workflow.
func breakTheRules(ctx context.Context, c *Chore) error {
	switch c.ID {
	case "shift":
		c.State = "todo"
	case "rekey":
		c.ID = "another"
	case "again":
		_, err := ChoreFlow.Fire(ctx, c.ID, "do")
		return err
	}
	return nil
}

// Badges are kept in a store with a unique index, under a workflow.
type Badge struct {
	ID     string `json:"id"`
	Holder string `json:"holder"`
	State  string `json:"state"`
}

var Badges = Hooks.Store("badges", func(b Badge) string { return b.ID },
	kit.Unique("holder", func(b Badge) string { return b.Holder }))

var BadgeFlow = Hooks.Workflow("badge", Badges, func(b *Badge) *string { return &b.State }).
	Initial("issued").
	On("revoke", "issued", "revoked")

func panicOnBoom(_ context.Context, c *Chore) error {
	if c.Boom && c.State == "done" {
		panic("an OnEnter hook's bug")
	}
	return nil
}

func panicAfterBoom(_ context.Context, ch kit.ChangeEvent[Chore, string]) error {
	if ch.Entity.ID == "after" {
		panic("an OnTransition hook's bug")
	}
	return nil
}

func noteAfter(_ context.Context, ch kit.ChangeEvent[Chore, string]) error {
	ranAfter <- ch.Entity.ID
	return nil
}

func startHooks(t *testing.T) *kit.App {
	t.Helper()
	return startHooksLogging(t, io.Discard)
}

func startHooksLogging(t *testing.T, logs io.Writer) *kit.App {
	t.Helper()
	app := kit.NewApp("hooks", Hooks).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(logs))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return app
}

// fireWithin fires event on key, failing the test if the workflow does not
// answer in time — a lock left held would block it forever.
//
// Goroutine lifecycle: one goroutine fires the event and reports on a buffered
// channel, which the helper waits on within its deadline.
func fireWithin(t *testing.T, key, event string) (Chore, error) {
	t.Helper()
	type result struct {
		c   Chore
		err error
	}
	done := make(chan result, 1)
	go func() {
		c, err := ChoreFlow.Fire(t.Context(), key, event)
		done <- result{c, err}
	}()
	select {
	case r := <-done:
		return r.c, r.err
	case <-time.After(5 * time.Second):
		t.Fatalf("Fire(%q, %q) never returned: the workflow is still locked", key, event)
		return Chore{}, nil
	}
}

// An OnEnter hook runs under the workflow's lock. When it panicked, the lock
// was never released and every later transition of the workflow blocked.
func TestAPanickingOnEnterHookFailsItsTransitionAndFreesTheWorkflow(t *testing.T) {
	startHooks(t)
	ctx := t.Context()
	for _, c := range []Chore{{ID: "boom", Boom: true}, {ID: "fine"}} {
		if _, err := ChoreFlow.Start(ctx, c); err != nil {
			t.Fatalf("start %s: %v", c.ID, err)
		}
	}

	_, err := fireWithin(t, "boom", "do")
	if !errs.HasCode(err, kit.CodeWorkflowHookPanic) {
		t.Fatalf("the panicking transition answered %v, want %v", err, kit.CodeWorkflowHookPanic)
	}
	if got, err := Chores.Get(ctx, "boom"); err != nil || got.State != "todo" {
		t.Errorf("the failed transition was stored: state %q, want todo", got.State)
	}

	fine, err := fireWithin(t, "fine", "do")
	if err != nil || fine.State != "done" {
		t.Fatalf("the next transition answered (%q, %v), want (done, nil)", fine.State, err)
	}
}

// An OnTransition hook runs after the transition is stored: a panic there
// leaves the transition standing and the hooks after it running. The Studio
// shows it as a problem naming the transition; the log has what panicked.
func TestAPanickingOnTransitionHookLeavesTheTransitionStanding(t *testing.T) {
	var logs syncBuffer
	app := startHooksLogging(t, &logs)
	ctx := t.Context()
	if _, err := ChoreFlow.Start(ctx, Chore{ID: "after"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	drain(ranAfter)

	c, err := fireWithin(t, "after", "do")
	if err != nil || c.State != "done" {
		t.Fatalf("the transition answered (%q, %v), want (done, nil)", c.State, err)
	}
	select {
	case id := <-ranAfter:
		if id != "after" {
			t.Errorf("the hook after the panicking one saw %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the OnTransition hook after the panicking one never ran")
	}
	var said bool
	for _, d := range app.Graph().Diagnostics {
		said = said || d.Node == "hooks/workflow/chore" && strings.HasPrefix(d.Message, `an OnTransition hook of hooks/workflow/chore failed after "do" on "after": `)
	}
	if !said {
		t.Errorf("no problem names the failed hook: %+v", app.Graph().Diagnostics)
	}
	if !strings.Contains(logs.String(), "a workflow hook panicked") || !strings.Contains(logs.String(), "an OnTransition hook's bug") {
		t.Errorf("the log does not say what panicked:\n%s", logs.String())
	}
}

func drain(c chan string) {
	for {
		select {
		case _, open := <-c:
			if !open {
				return
			}
		default:
			return
		}
	}
}

// An OnEnter hook that changes the state it enters or its entity's key, or
// fires its own workflow, fails its transition — refused where the last one
// used to wait for itself forever — and nothing is stored.
func TestAnOnEnterHookThatBreaksTheRulesFailsItsTransition(t *testing.T) {
	startHooks(t)
	ctx := t.Context()
	for id, want := range map[string]errs.Code{"shift": kit.CodeWorkflowHookChange, "rekey": kit.CodeWorkflowHookChange, "again": kit.CodeWorkflowReentrant} {
		if _, err := ChoreFlow.Start(ctx, Chore{ID: id}); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
		if _, err := fireWithin(t, id, "do"); !errs.HasCode(err, want) {
			t.Errorf("%s: the transition answered %v, want %v", id, err, want)
		}
		if got, err := Chores.Get(ctx, id); err != nil || got.State != "todo" {
			t.Errorf("%s: the refused transition was stored: %+v %v", id, got, err)
		}
	}
	if _, err := Chores.Get(ctx, "another"); err == nil {
		t.Error("a hook's new key was stored")
	}
}

// What the store refuses reaches the caller in the store's words, through
// the engine: a key taken and a unique index taken are each a Conflict, an
// entity missing a NotFound.
func TestAWorkflowSpeaksForItsStore(t *testing.T) {
	startHooks(t)
	ctx := t.Context()
	if _, err := BadgeFlow.Start(ctx, Badge{ID: "b1", Holder: "ann"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		badge Badge
		want  string
	}{
		{Badge{ID: "b1", Holder: "bob"}, `badges: an entity with key "b1" already exists`},
		{Badge{ID: "b2", Holder: "ann"}, `badges: another entity already has this key in the unique index "holder"`},
	} {
		_, err := BadgeFlow.Start(ctx, c.badge)
		var ke *kit.Error
		if !errors.As(err, &ke) || ke.Code != kit.WireConflict || ke.Message != c.want {
			t.Errorf("Start(%+v) = %v, want the conflict %q", c.badge, err, c.want)
		}
	}
	_, err := BadgeFlow.Fire(ctx, "nobody", "revoke")
	var ke *kit.Error
	if !errors.As(err, &ke) || ke.Code != kit.WireNotFound || ke.Message != `badges: no entity with key "nobody"` {
		t.Errorf("Fire on a missing badge = %v", err)
	}
	if _, err := BadgeFlow.Fire(ctx, "b1", "revoke"); err != nil {
		t.Fatal(err)
	}
	_, err = BadgeFlow.Fire(ctx, "b1", "revoke")
	if !errors.As(err, &ke) || ke.Code != kit.WireConflict || ke.Message != `badge: "revoke" is not possible from state "revoked"` {
		t.Errorf("a second revoke = %v", err)
	}
	if counts, err := BadgeFlow.Census(ctx); err != nil || counts["issued"] != 0 || counts["revoked"] != 1 {
		t.Errorf("census %v %v", counts, err)
	}
}
