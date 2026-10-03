package kit_test

import (
	"context"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// startCounterManual runs the counter on a manual clock: a queued command's retry
// waits on it.
func startCounterManual(t *testing.T) (*kit.App, *clock.ManualClock) {
	t.Helper()
	resetLab()
	clk := clock.NewManualClock(time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC))
	return startCounter(t, kit.Clock(clk)), clk
}

// resetLab forgets what the queued lab command did, and fails nothing.
func resetLab() {
	labFailures.Store(0)
	labHandled.Lock()
	labHandled.users = nil
	labHandled.Unlock()
}

// handledBy is who the queued command's handled runs acted for, so far.
func handledBy() []kit.UID {
	labHandled.Lock()
	defer labHandled.Unlock()
	return slices.Clone(labHandled.users)
}

// labStats are the queued command's node counters.
func labStats(app *kit.App) (count, errors int64) {
	if s := app.Graph().Node("lab/command/queued").Stats; s != nil {
		return s.Count, s.Errors
	}
	return 0, 0
}

// A queued command's Dispatch returns once its queue accepted it; its
// consumer handles it as the user who dispatched it.
func TestAQueuedCommandIsHandledAsItsDispatcher(t *testing.T) {
	app, _ := startCounterManual(t)
	if _, err := LabQueued.Dispatch(as(t.Context(), "alice"), LabInput{Key: "q1"}); err != nil {
		t.Fatalf("the dispatch: %v", err)
	}
	eventually(t, "the handling", func() bool { return len(handledBy()) == 1 })
	if got := handledBy(); got[0] != "alice" {
		t.Errorf("the handler acted for %q, want alice", got[0])
	}
	eventually(t, "the run counted", func() bool { return loopOf(t, app, "lab/command/queued consumer").Runs == 1 })
	if l := loopOf(t, app, "lab/command/queued consumer"); l.Kind != model.LoopConsumer || l.Library != "sdk/v1/data/queue" {
		t.Errorf("the consumer's loop: %+v", l)
	}
}

// handledTrace is the one trace of the queued lab command, once it holds
// the dispatch, its handling and the handling's transaction.
func handledTrace(t *testing.T, app *kit.App) model.Trace {
	t.Helper()
	var traces []model.Trace
	eventually(t, "the handling's span", func() bool {
		call(t, app, "GET /_kit/api/traces?node=lab/command/queued", noBody).json(t, &traces)
		return len(traces) == 1 && len(traces[0].Spans) == 3
	})
	return traces[0]
}

// spanByOp is the one span of tr whose operation is op.
func spanByOp(t *testing.T, tr *model.Trace, op string) model.Span {
	t.Helper()
	for _, s := range tr.Spans {
		if s.Op == op {
			return s
		}
	}
	t.Fatalf("no %s span in %+v", op, tr.Spans)
	return model.Span{}
}

// A queued command's handling continues the dispatcher's trace, as a
// delivery does: under the dispatch, from the node that dispatched it, no
// edge drawn twice, its key the span's instance; its handler runs in a
// transaction under it.
func TestAQueuedCommandIsHandledInTheDispatchersTrace(t *testing.T) {
	app, _ := startCounterManual(t)
	if _, err := LabQueued.Dispatch(as(t.Context(), "alice"), LabInput{Key: "q1"}); err != nil {
		t.Fatalf("the dispatch: %v", err)
	}
	tr := handledTrace(t, app)
	// The handling may be recorded before the dispatch's span closes: the
	// spans are found by their operation, never by the order they were kept.
	dispatched, handled, tx := spanByOp(t, &tr, model.OpDispatch), spanByOp(t, &tr, model.OpHandle), spanByOp(t, &tr, model.OpTransaction)
	if dispatched.Op != model.OpDispatch || handled.Op != model.OpHandle || handled.ParentID != dispatched.SpanID || handled.Edge != "" {
		t.Errorf("the dispatch %+v, then its handling %+v", dispatched, handled)
	}
	if handled.User != "alice" || handled.Attrs["instance"] != "q1" || handled.Attrs["delivery"] != "1" {
		t.Errorf("the handling's user and attributes: %+v", handled)
	}
	if tx.Op != model.OpTransaction || tx.ParentID != handled.SpanID || tx.Attrs["outcome"] != model.OutcomeCommit {
		t.Errorf("the handling's transaction: %+v", tx)
	}
}

// A handling that fails is retried after the queue's delay, then succeeds;
// one that keeps failing is dead-lettered after its last attempt — counted
// on the node — and gives its key back.
func TestAQueuedCommandIsRetriedThenDeadLettered(t *testing.T) {
	app, clk := startCounterManual(t)
	labFailures.Store(1)
	if _, err := LabQueued.Dispatch(t.Context(), LabInput{Key: "flaky"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first attempt", func() bool { _, errs := labStats(app); return errs == 1 })
	eventually(t, "the retry", func() bool {
		clk.Advance(500 * time.Millisecond)
		return len(handledBy()) == 1
	})
	labFailures.Store(3)
	if _, err := LabQueued.Dispatch(t.Context(), LabInput{Key: "doomed"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "three attempts, then the dead letter", func() bool {
		clk.Advance(500 * time.Millisecond)
		dead := app.Graph().Node("lab/command/queued").Command.DeadLetters
		return dead != nil && *dead == 1
	})
	if got := handledBy(); len(got) != 1 {
		t.Errorf("a doomed command was handled: %v", got)
	}
	if _, errs := labStats(app); errs != 4 {
		t.Errorf("failed attempts: %d, want 4", errs)
	}
	// Dead-lettered, its key is free: the same key is queued again.
	if _, err := LabQueued.Dispatch(t.Context(), LabInput{Key: "doomed"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the key queued again", func() bool { return len(handledBy()) == 2 })
}

// A queued command whose key waits or runs is not queued again: the second
// dispatch is accepted, and says it was deduplicated.
func TestAQueuedKeyIsNotQueuedTwice(t *testing.T) {
	app, _ := startCounterManual(t)
	labQueued.hold()
	defer labQueued.release()
	for range 3 {
		if _, err := LabQueued.Dispatch(t.Context(), LabInput{Key: "once"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LabQueued.Dispatch(t.Context(), LabInput{Key: "other"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both keys running", func() bool { return labQueued.running.Load() == 2 })
	labQueued.release()
	eventually(t, "the handlings", func() bool { return len(handledBy()) == 2 })
	time.Sleep(50 * time.Millisecond)
	if got := handledBy(); len(got) != 2 {
		t.Errorf("handled %d times, want once per key", len(got))
	}
	var traces []model.Trace
	call(t, app, "GET /_kit/api/traces?node=lab/command/queued", noBody).json(t, &traces)
	deduplicated := 0
	for _, tr := range traces {
		for _, s := range tr.Spans {
			if s.Op == model.OpDispatch && s.Attrs["deduplicated"] == "true" {
				deduplicated++
			}
		}
	}
	if deduplicated != 2 {
		t.Errorf("deduplicated dispatches: %d, want 2", deduplicated)
	}
}

// A queued command is checked at its dispatch, as its caller: an invalid
// input, a refused caller, a key that names nothing are the caller's
// answer, and nothing is queued.
func TestAQueuedCommandIsCheckedAtItsDispatch(t *testing.T) {
	startCounterManual(t)
	_, err := CounterReindex.Dispatch(as(t.Context(), "alice"), kit.EmptyValue{})
	expectRefused(t, err, http.StatusForbidden, kit.WireForbidden, "alice reindexes")
	_, err = CounterReindex.Dispatch(t.Context(), kit.EmptyValue{})
	expectRefused(t, err, http.StatusUnauthorized, kit.WireUnauth, "a reindex without a user")
	_, err = LabQueued.Dispatch(t.Context(), LabInput{})
	expectInvalid(t, err, "a queued command whose input names no key")
	counterReindexed.Lock()
	counterReindexed.by = nil
	counterReindexed.Unlock()
	if _, err := CounterReindex.Dispatch(as(t.Context(), "root"), kit.EmptyValue{}); err != nil {
		t.Fatalf("root reindexes: %v", err)
	}
	eventually(t, "the reindex", func() bool {
		counterReindexed.Lock()
		defer counterReindexed.Unlock()
		return slices.Equal(counterReindexed.by, []kit.UID{"root"})
	})
}

// startOn starts the counter on the data directory dir and clk — the system's
// when nil — outside dev, and stops it with the test.
func startOn(t *testing.T, dir string, clk clock.Timed) *kit.App {
	t.Helper()
	opts := []kit.AppConfigurer{kit.DataDir(dir), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)}
	if clk != nil {
		opts = append(opts, kit.Clock(clk))
	}
	app := kit.NewApp("counter", Staff, Counter, Lab).With(opts...)
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	return app
}

// A queued command waits in a file queue under the data directory, and what
// is queued when the app stops is handled at its next start: here, a
// dispatch whose first attempt failed, its retry due after the stop.
func TestAQueuedCommandSurvivesARestart(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	resetLab()
	labFailures.Store(1)
	first := startOn(t, dir, clock.NewManualClock(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)))
	if _, err := LabQueued.Dispatch(t.Context(), LabInput{Key: "kept"}); err != nil {
		t.Fatal(err)
	}
	// The attempt takes the failure; the consumer then nacks it, its retry
	// due on a clock that stands still.
	eventually(t, "the first attempt", func() bool { return labFailures.Load() == 0 })
	time.Sleep(20 * time.Millisecond)
	if q := first.Graph().Node("lab/command/queued").Command.Queue; q != "file" {
		t.Errorf("the queue is %q, want file", q)
	}
	if err := first.Stop(t.Context()); err != nil || len(handledBy()) != 0 {
		t.Fatalf("the stop, before the retry: %v, handled %v", err, handledBy())
	}
	startOn(t, dir, nil)
	eventually(t, "the retry after the restart", func() bool { return len(handledBy()) == 1 })
}
