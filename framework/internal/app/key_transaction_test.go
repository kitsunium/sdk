package app_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/app/lock"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A keyed command dispatched inside another command's transaction (issue
// #275): its run ends — and its key's lease with it — before the transaction
// around it commits, so what it held until the commit must not leave with a
// context the end of its run cancelled. The loss of its lease, while it
// runs, must still reach its handler.

// dossier is a desk's case: a folder keys the command that issues it.
type dossier struct {
	ID     string `json:"id"`
	Folder string `json:"folder"`
	State  string `json:"state"`
}

// decision is what the transactional command decides: the dossiers it
// issues, and whether it fails once they are.
type decision struct {
	Dossiers []dossier `json:"dossiers"`
	Fail     bool      `json:"fail"`
}

// hookSeen is one OnTransition hook's run: the dossier, and its context's
// error as the hook found it.
type hookSeen struct {
	id  string
	err error
}

// desk is a product of one service, declared afresh for each test: decide,
// transactional, dispatches issue, keyed by folder, which starts a dossier's
// workflow, fires it, and publishes; the workflow's OnTransition hook
// publishes too, with the context it was handed.
type desk struct {
	svc            *app.Service
	files          *app.StoreService[dossier]
	flow           *app.WorkflowService[dossier, string]
	issued, hooked *app.TopicService[dossier]
	heardIssued    *heardIDs
	heardHooked    *heardIDs
	seen           chan hookSeen
	issue          *app.Command[dossier, app.EmptyValue]
	decide         *app.Command[decision, app.EmptyValue]
	// running and most count the issue runs at once.
	running, most atomic.Int32
	// linger, when set, holds an issue run until its context ends, and
	// receives the context's cause.
	linger chan error
	// lingering is told when a lingering run waits.
	lingering chan struct{}
}

// errDecided fails a decision after it issued.
var errDecided = errors.New("the decision failed after it issued")

func newDesk() *desk {
	d := &desk{
		svc:         app.NewService("desk", "A desk, for the keyed commands' transactions."),
		heardIssued: &heardIDs{}, heardHooked: &heardIDs{},
		seen: make(chan hookSeen, 64), lingering: make(chan struct{}, 1),
	}
	d.files = d.svc.Store("files", func(f dossier) string { return f.ID })
	d.issued = d.svc.Topic[dossier]("issued")
	d.hooked = d.svc.Topic[dossier]("hooked")
	d.svc.Subscribe("heard-issued", d.issued, func(_ context.Context, f dossier) error { d.heardIssued.add(f.ID); return nil })
	d.svc.Subscribe("heard-hooked", d.hooked, func(_ context.Context, f dossier) error { d.heardHooked.add(f.ID); return nil })
	d.flow = d.svc.Workflow("dossier", d.files, func(f *dossier) *string { return &f.State }).
		Initial("open").
		On("issue", "open", "issued").
		OnTransition(func(ctx context.Context, ch app.ChangeEvent[dossier, string]) error {
			if ch.Event != "issue" {
				return nil
			}
			d.seen <- hookSeen{id: ch.Entity.ID, err: ctx.Err()}
			return d.hooked.Publish(ctx, ch.Entity)
		})
	d.issue = d.svc.Command("issue", d.issueOne).Key(func(f dossier) string { return f.Folder })
	d.decide = d.svc.Command("decide", func(ctx context.Context, in decision) (app.EmptyValue, error) {
		for _, f := range in.Dossiers {
			if _, err := d.issue.Dispatch(ctx, f); err != nil {
				return app.EmptyValue{}, err
			}
		}
		if in.Fail {
			return app.EmptyValue{}, errDecided
		}
		return app.EmptyValue{}, nil
	})
	return d
}

// issueOne is issue's handler.
func (d *desk) issueOne(ctx context.Context, f dossier) (app.EmptyValue, error) {
	now := d.running.Add(1)
	defer d.running.Add(-1)
	for most := d.most.Load(); now > most && !d.most.CompareAndSwap(most, now); most = d.most.Load() {
	}
	if _, err := d.flow.Start(ctx, dossier{ID: f.ID, Folder: f.Folder}); err != nil {
		return app.EmptyValue{}, err
	}
	if _, err := d.flow.Fire(ctx, f.ID, "issue"); err != nil {
		return app.EmptyValue{}, err
	}
	if err := d.issued.Publish(ctx, f); err != nil {
		return app.EmptyValue{}, err
	}
	if d.linger != nil {
		d.lingering <- struct{}{}
		<-ctx.Done()
		d.linger <- context.Cause(ctx)
		return app.EmptyValue{}, ctx.Err()
	}
	// The run gives others with its key a chance to overlap it, which
	// they must not.
	time.Sleep(time.Millisecond)
	return app.EmptyValue{}, nil
}

// runDesk runs the desk in dev on opts until the test ends. The app is named
// till, so the backends' database variable names its database.
func runDesk(t *testing.T, d *desk, opts ...app.AppConfigurer) *app.App {
	t.Helper()
	t.Setenv("KIT_SECRETS", "memory")
	a := app.NewApp("till", d.svc).With(append([]app.AppConfigurer{
		app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard),
	}, opts...)...)
	run(t, a)
	return a
}

// hooks waits for n OnTransition hooks and returns them.
func (d *desk) hooks(t *testing.T, n int) []hookSeen {
	t.Helper()
	var got []hookSeen
	for range n {
		select {
		case h := <-d.seen:
			got = append(got, h)
		case <-time.After(5 * time.Second):
			t.Fatalf("%d of %d OnTransition hooks ran", len(got), n)
		}
	}
	return got
}

// A keyed command dispatched by a transactional one holds its effects until
// the outer commit, and they leave then: its publish is delivered, and its
// workflow's OnTransition hook runs with a context its run's end did not
// cancel — so what the hook publishes is delivered too. Nested twice, the
// same.
func TestAKeyedCommandsEffectsLeaveAtTheOuterCommit(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			d := newDesk()
			runDesk(t, d, b.opts(t)...)
			in := decision{Dossiers: []dossier{{ID: "d1", Folder: "f1"}, {ID: "d2", Folder: "f2"}}}
			if _, err := d.decide.Dispatch(t.Context(), in); err != nil {
				t.Fatalf("decide: %v", err)
			}
			for _, h := range d.hooks(t, 2) {
				if h.err != nil {
					t.Errorf("the hook of %s ran with a context already ended: %v", h.id, h.err)
				}
			}
			eventually(t, "the keyed command's publishes", func() bool {
				return slices.Contains(d.heardIssued.list(), "d1") && slices.Contains(d.heardIssued.list(), "d2")
			})
			eventually(t, "the hooks' publishes", func() bool {
				return slices.Contains(d.heardHooked.list(), "d1") && slices.Contains(d.heardHooked.list(), "d2")
			})
			// One level deeper: an app.Transact around the decision.
			err := app.Transact(t.Context(), func(ctx context.Context) error {
				_, err := d.decide.Dispatch(ctx, decision{Dossiers: []dossier{{ID: "d3", Folder: "f1"}}})
				return err
			})
			if err != nil {
				t.Fatalf("decide in a transaction: %v", err)
			}
			if h := d.hooks(t, 1)[0]; h.err != nil {
				t.Errorf("nested twice, the hook ran with a context already ended: %v", h.err)
			}
			eventually(t, "the publishes nested twice", func() bool {
				return slices.Contains(d.heardIssued.list(), "d3") && slices.Contains(d.heardHooked.list(), "d3")
			})
		})
	}
}

// Until the outer commit nothing leaves: the keyed command's publish and its
// hook wait for it, while the keyed run itself is over. The transaction runs
// in a goroutine, held open by release, and reports on ran, which the test
// reads before it returns.
func TestAKeyedCommandsEffectsWaitForTheOuterCommit(t *testing.T) {
	d := newDesk()
	runDesk(t, d, app.InMemory())
	dispatched, release := make(chan struct{}), make(chan struct{})
	ran := make(chan error, 1)
	go func() {
		ran <- app.Transact(context.WithoutCancel(t.Context()), func(ctx context.Context) error {
			// Dispatch returns once the keyed run is over: its key given
			// back, its keepalive stopped.
			if _, err := d.issue.Dispatch(ctx, dossier{ID: "held", Folder: "f"}); err != nil {
				return err
			}
			close(dispatched)
			<-release
			return nil
		})
	}()
	select {
	case <-dispatched:
	case <-time.After(5 * time.Second):
		t.Fatal("the keyed command never returned")
	}
	// What a wrong release would have sent has the time to arrive.
	time.Sleep(50 * time.Millisecond)
	select {
	case h := <-d.seen:
		t.Errorf("the hook of %s ran before the outer commit", h.id)
	default:
	}
	if slices.Contains(d.heardIssued.list(), "held") {
		t.Error("the keyed command's publish left before the outer commit")
	}
	close(release)
	if err := <-ran; err != nil {
		t.Fatalf("the outer transaction: %v", err)
	}
	if h := d.hooks(t, 1)[0]; h.err != nil {
		t.Errorf("the held hook ran with a context already ended: %v", h.err)
	}
	eventually(t, "the held publish and hook", func() bool {
		return slices.Contains(d.heardIssued.list(), "held") && slices.Contains(d.heardHooked.list(), "held")
	})
}

// A rollback of the outer transaction drops what the keyed command held,
// though its own run committed its savepoint.
func TestARolledBackDecisionDropsTheKeyedCommandsEffects(t *testing.T) {
	d := newDesk()
	runDesk(t, d, app.InMemory())
	_, err := d.decide.Dispatch(t.Context(), decision{Dossiers: []dossier{{ID: "dropped", Folder: "f"}}, Fail: true})
	if !errors.Is(err, errDecided) {
		t.Fatalf("the failed decision answered %v", err)
	}
	if _, err := d.decide.Dispatch(t.Context(), decision{Dossiers: []dossier{{ID: "kept", Folder: "f"}}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the next decision's effects", func() bool {
		return slices.Contains(d.heardIssued.list(), "kept") && slices.Contains(d.heardHooked.list(), "kept")
	})
	if slices.Contains(d.heardIssued.list(), "dropped") || slices.Contains(d.heardHooked.list(), "dropped") {
		t.Error("a rolled back decision's effects were delivered")
	}
	if _, err := d.files.Get(t.Context(), "dropped"); err == nil {
		t.Error("a rolled back decision's dossier stayed")
	}
}

// Decisions at once over one folder never run its key twice at once, and
// every one's effects leave with a live context.
func TestDecisionsOnOneKeyAtOnce(t *testing.T) {
	d := newDesk()
	runDesk(t, d, app.InMemory())
	const decisions int = 8
	var wg sync.WaitGroup
	for i := range decisions {
		wg.Go(func() {
			in := decision{Dossiers: []dossier{{ID: "c" + string(rune('a'+i)), Folder: "shared"}}}
			if _, err := d.decide.Dispatch(t.Context(), in); err != nil {
				t.Errorf("decision %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	for _, h := range d.hooks(t, decisions) {
		if h.err != nil {
			t.Errorf("the hook of %s ran with a context already ended: %v", h.id, h.err)
		}
	}
	if most := d.most.Load(); most != 1 {
		t.Errorf("one key ran %d times at once", most)
	}
	eventually(t, "every decision's effects", func() bool {
		return len(d.heardIssued.list()) == decisions && len(d.heardHooked.list()) == decisions
	})
}

// The loss of a keyed run's lease still reaches its handler, inside a
// transaction too: its context ends with the keepalive's cause, its error
// rolls its savepoint back, and what it held is dropped. The decision is
// dispatched from a goroutine that reports on ran, which the test reads once
// the lease is lost.
func TestALostLeaseStillEndsTheKeyedRun(t *testing.T) {
	d := newDesk()
	d.linger = make(chan error, 1)
	clk := clock.NewManualClock(time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	runDesk(t, d, app.InMemory(), app.Clock(clk))
	ran := make(chan error, 1)
	go func() {
		_, err := d.decide.Dispatch(context.WithoutCancel(t.Context()), decision{Dossiers: []dossier{{ID: "lost", Folder: "f"}}})
		ran <- err
	}()
	select {
	case <-d.lingering:
	case <-time.After(5 * time.Second):
		t.Fatal("the keyed run never started")
	}
	// Past the lease's 30 s: the next renewal finds it lapsed.
	clk.Advance(31 * time.Second)
	select {
	case cause := <-d.linger:
		if !errs.HasCode(cause, lock.LockKeepaliveLost.Code()) {
			t.Errorf("the keyed run's context ended with %v, want the lease's loss", cause)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lost lease never reached the handler")
	}
	if err := <-ran; err == nil {
		t.Error("a decision whose keyed run lost its lease succeeded")
	}
	d.linger = nil
	if _, err := d.decide.Dispatch(t.Context(), decision{Dossiers: []dossier{{ID: "after", Folder: "f"}}}); err != nil {
		t.Fatalf("the key after its lost lease: %v", err)
	}
	eventually(t, "the next decision's effects", func() bool { return slices.Contains(d.heardIssued.list(), "after") })
	if slices.Contains(d.heardIssued.list(), "lost") || slices.Contains(d.heardHooked.list(), "lost") {
		t.Error("the effects of a run that lost its lease were delivered")
	}
}
