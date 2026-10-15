package kit

import (
	"io"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// binPage is a page of the trash's internal tests.
type binPage struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

var binSvc = NewService("bin", "Pages that keep their revisions and a trash, for the trash's internal tests (ADR 0178 C4).")

var binPages = binSvc.Store("pages", func(p binPage) string { return p.ID }, Revisions(2))

// A purge empties only the tombstone the agenda scheduled: a page restored
// and deleted again since is a tombstone trashed later, which waits for its
// own instant, whoever reads the agenda's old entry.
func TestAPurgeEmptiesOnlyTheTombstoneItScheduled(t *testing.T) {
	epoch := time.Date(2026, time.October, 1, 9, 0, 0, 0, time.UTC)
	clk := clock.NewManualClock(epoch)
	app := NewApp("bin", binSvc).With(InMemory(), Clock(clk), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	ctx := t.Context()
	must(t, app.Start(ctx))
	t.Cleanup(func() {
		if err := app.Stop(t.Context()); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	must(t, binPages.Insert(ctx, binPage{ID: "p", Body: "one"}))
	must(t, binPages.Delete(ctx, "p"))
	clk.Advance(time.Hour)
	_, err := binPages.Untrash(ctx, "p")
	must(t, err)
	must(t, binPages.Delete(ctx, "p"))
	if done, err := binPages.purgeTrashed(ctx, app, "p", epoch); err != nil || done {
		t.Errorf("a purge of the first deletion's tombstone: %v %v, want none", done, err)
	}
	if stones, err := binPages.Trashed(ctx); err != nil || len(stones) != 1 || !stones[0].At.Equal(epoch.Add(time.Hour)) {
		t.Fatalf("the trash after the stale purge: %+v %v", stones, err)
	}
	if done, err := binPages.purgeTrashed(ctx, app, "p", epoch.Add(time.Hour)); err != nil || !done {
		t.Errorf("a purge of the tombstone scheduled: %v %v", done, err)
	}
}
