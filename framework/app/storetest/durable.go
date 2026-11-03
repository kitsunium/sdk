package storetest

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
)

// The cases that stop the product and start it again on what it left (#998):
// a watch's catch-up, a workflow's journal, a copy of the data directory
// into the database. Each runs its apps on the case's placement — the
// backend's options, built once per case and given to every app of it, so
// the same data directory and the same database —, and skips on a backend
// that keeps nothing past a stop.

// backend is where the case's documents are kept: "memory", "file", or the
// database's engine.
func (s *suite) backend() string {
	node := s.running.Graph().Node("st-" + s.name + "/store/docs")
	if node == nil || node.Store == nil {
		return ""
	}
	return node.Store.Backend
}

// onDatabase reports whether the case's stores are on a database.
func (s *suite) onDatabase() bool {
	backend := s.backend()
	return backend != "memory" && backend != "file" && backend != ""
}

// durable skips the case on a backend that keeps nothing past a stop.
func durable(t *testing.T, s *suite) {
	t.Helper()
	if s.backend() == "memory" {
		t.Skip("memory keeps nothing past a stop")
	}
}

// again stops s's product and builds the case's service again, declared by
// declare, on the same placement and the same clock: the product a restart
// finds.
func (s *suite) again(t *testing.T, declare func(*app.Service, *suite)) *suite {
	t.Helper()
	s.stop(t)
	return build(s.name, s.place, s.clock, declare)
}

// watchCatchUp checks a watch's replay on the backend (ADR 0178 C6): memos
// written while no watch heard them — a run that mounted none and stopped
// cleanly — are queued again by `watch replay` and heard at the next start;
// after a clean stop, the start after it hears nothing again.
func watchCatchUp(t *testing.T, s *suite) {
	durable(t, s)
	ctx := t.Context()
	for _, id := range []string{"m2", "m1"} {
		must(t, s.memos.Insert(ctx, memo{ID: id, Text: "unheard"}))
	}
	replaying := s.again(t, declareMemos)
	if exit := replaying.running.Main(ctx, []string{"watch", "replay", "st-" + s.name + "/subscription/screen"}); exit != 0 {
		t.Fatalf("watch replay exited %d, want 0", exit)
	}
	if exit := replaying.running.Main(ctx, []string{"watch", "replay", "st-" + s.name + "/subscription/nothing"}); exit != 1 {
		t.Errorf("watch replay of no watch exited %d, want 1", exit)
	}
	heard := build(s.name, s.place, s.clock, declareMemos)
	heard.start(t)
	heard.settle(t, "after")
	for _, id := range []string{"m1", "m2"} {
		if !heard.listed.has(id) {
			t.Errorf("after watch replay the watch did not hear %s", id)
		}
	}
	next := heard.again(t, declareMemos)
	next.start(t)
	next.settle(t, "next")
	if next.listed.has("m1") || next.listed.has("after") {
		t.Error("a memo heard before a clean stop was heard again")
	}
}

// declareTimers declares a store a workflow runs over whose one transition
// is a timer: an hour after it enters "new", an entity expires.
func declareTimers(svc *app.Service, s *suite) {
	s.timers = svc.Store("timers", doc.key)
	s.expiry = svc.Workflow("expiry", s.timers, stateOf).Initial("new").After("expire", time.Hour, "new", "expired")
}

// workflowJournal checks a workflow's timer on the backend's journal: a
// timer armed before a stop fires after the next start, at its instant on
// the app's clock and not a second before.
func workflowJournal(t *testing.T, s *suite) {
	durable(t, s)
	ctx := t.Context()
	if _, err := s.expiry.Start(ctx, doc{ID: "t1"}); err != nil {
		t.Fatalf("start the entity: %v", err)
	}
	restarted := s.again(t, declareTimers)
	restarted.start(t)
	s.clock.Advance(time.Hour - time.Second)
	time.Sleep(10 * pollEvery)
	if d, err := restarted.timers.Get(ctx, "t1"); err != nil || d.State != "new" {
		t.Fatalf("before its instant: %+v %v, want the entity still new", d, err)
	}
	ticking(t, restarted, "the timer armed before the restart", func() bool {
		d, err := restarted.timers.Get(ctx, "t1")
		return err == nil && d.State == "expired"
	})
}

var (
	// copyArgs is the command line of a copy of the data directory into the
	// case's database.
	copyArgs = []string{"data", "copy", "--to", "database"}
	// copiedMemos are the memos the data-copy case keeps in files, then
	// copies.
	copiedMemos = []string{"c1", "c2"}
)

// dataCopy checks `data copy --to` (ADR 0178 C7) into the backend's
// database: a product that kept its memos in its data directory declares the
// database and copies them — the copy verifies their fingerprints —, reads
// them back from it, and a second copy into a database that holds them is
// refused.
func dataCopy(t *testing.T, s *suite) {
	if !s.onDatabase() {
		t.Skip("no database to copy into")
	}
	ctx := t.Context()
	s.stop(t)
	dir := t.TempDir()
	files := build(s.name, []app.AppOption{app.DataDir(dir)}, nil, declareMemoStore)
	files.start(t)
	for _, id := range copiedMemos {
		must(t, files.memos.Insert(ctx, memo{ID: id, Text: "copied"}))
	}
	files.stop(t)
	into := append(slices.Clone(s.place), app.DataDir(dir))
	if exit := build(s.name, into, nil, declareMemoStore).running.Main(ctx, copyArgs); exit != 0 {
		t.Fatalf("data copy exited %d, want 0", exit)
	}
	if exit := build(s.name, into, nil, declareMemoStore).running.Main(ctx, copyArgs); exit == 0 {
		t.Error("a second copy into a database that holds the memos was not refused")
	}
	after := build(s.name, into, nil, declareMemoStore)
	after.start(t)
	expectCopied(t, after)
}

// expectCopied checks that the copied memos read back from the database.
func expectCopied(t *testing.T, s *suite) {
	t.Helper()
	for _, id := range copiedMemos {
		if got, err := s.memos.Get(t.Context(), id); err != nil || got.Text != "copied" {
			t.Errorf("%s after the copy: %+v %v", id, got, err)
		}
	}
	if !s.onDatabase() {
		t.Errorf("after the copy the memos are kept in %q, want the database", s.backend())
	}
}

// otlpPlace is the case's placement with an OTLP export whose queues lie on
// the database — a table per signal the framework's own migrations make
// (#537) —, sent by send, an export a second on the app's clock.
func (s *suite) otlpPlace(send func(context.Context, app.OTLPBatch) error) []app.AppOption {
	return append(slices.Clone(s.place),
		app.OTLP(app.OTLPDatabase("database"), app.OTLPSender(send), app.OTLPSendTimeout(time.Hour)),
		app.Set("otlp-drain", time.Duration(0)), app.Set("otlp-interval", time.Second))
}

// exports counts the batches an OTLP sender was handed, by outcome.
type exports struct {
	refused, sent atomic.Int64
}

// refuse is a collector that is down: every batch waits for the next try.
func (e *exports) refuse(_ context.Context, _ app.OTLPBatch) error {
	e.refused.Add(1)
	return app.Unavailable("the collector is down")
}

// accept is a collector that takes every batch.
func (e *exports) accept(_ context.Context, _ app.OTLPBatch) error {
	e.sent.Add(1)
	return nil
}

// otlpTables checks the OTLP export's queues on the backend's database
// (otlp-database, #537): a batch a collector that is down refused waits in
// the export's table — the export's runtime state says its queues lie there,
// durably —, and outlives a stop: the next start sends it.
func otlpTables(t *testing.T, s *suite) {
	if !s.onDatabase() {
		t.Skip("no database to keep the export's queues")
	}
	s.stop(t)
	down := &exports{}
	first := build(s.name, s.otlpPlace(down.refuse), s.clock, nil)
	first.start(t)
	must(t, first.docs.Put(t.Context(), doc{ID: "traced"}))
	ticking(t, first, "a batch refused by a collector that is down", func() bool { return down.refused.Load() > 0 })
	export := first.running.Graph().Runtime.Export
	if export == nil || len(export.Signals) == 0 || export.Signals[0].Store != "database:database" || !export.Signals[0].Durable {
		t.Fatalf("the export's queues: %+v, want them on the database, durable", export)
	}
	first.stop(t)
	up := &exports{}
	second := build(s.name, s.otlpPlace(up.accept), s.clock, nil)
	second.start(t)
	ticking(t, second, "the first run's backlog sent by the second", func() bool { return up.sent.Load() > 0 })
}
