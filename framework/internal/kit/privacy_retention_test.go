package kit_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// The retention (ADR 0006 §5), on a manual clock: erased then deleted at
// its instants, never run with nothing due, stopped by a hold, a dry run
// that changes nothing, and off warned of. A test moves the clock once the
// reports' loop sleeps on its timer, the one wait armed on the privacy
// app's clock (armed).

// Retention, on a manual clock: nothing is due while a report is open; its
// personal data is erased at its instant, the report deleted at the next;
// an invoice is kept until its own.
func TestRetentionErasesThenDeletes(t *testing.T) {
	clk := clock.NewManualClock(epoch)
	app := startPrivacy(t, kit.Clock(clk))
	ctx := t.Context()
	must(t, Reports.Insert(ctx, Report{ID: "open", Email: "a@x.dev", Name: "A"}))
	must(t, Reports.Insert(ctx, Report{ID: "closed", Email: "b@x.dev", Name: "B", ClosedAt: at(0)}))
	eventually(t, "the erasure's instant on the agenda", func() bool {
		l := retentionLoop(t, app, "desk/store/reports retention")
		return l.NextRun != nil && l.NextRun.Equal(epoch.Add(90*24*time.Hour)) && l.Schedule == "erase after 90 days · delete after 365 days"
	})
	armed(t, clk, 1)
	clk.Advance(90*24*time.Hour + time.Second)
	eventually(t, "the erasure", func() bool {
		r, rErr := Reports.Get(ctx, "closed")
		if rErr != nil {
			t.Fatal(rErr)
		}
		return r.Name == "" && r.ErasedAt != nil
	})
	if r, err := Reports.Get(ctx, "open"); err != nil || r.Name != "A" {
		t.Error("an open report was erased")
	}
	eventually(t, "the next instant", func() bool {
		l := retentionLoop(t, app, "desk/store/reports retention")
		return l.NextRun != nil && l.NextRun.Equal(epoch.Add(365*24*time.Hour))
	})
	armed(t, clk, 1)
	clk.Advance(275 * 24 * time.Hour)
	eventually(t, "the deletion", func() bool {
		_, err := Reports.Get(ctx, "closed")
		return err != nil
	})
	var ops []string
	for _, e := range privacyOf(t, app).Journal {
		if e.Store == "desk/store/reports" {
			ops = append(ops, e.Op+"/"+e.By)
		}
	}
	if !slices.Equal(ops, []string{"delete/retention", "erase/retention"}) {
		t.Errorf("journal: %v", ops)
	}
}

// A store with nothing due never runs its retention: no poll.
func TestAnIdleRetentionDoesNotRun(t *testing.T) {
	clk := clock.NewManualClock(epoch)
	app := startPrivacy(t, kit.Clock(clk))
	must(t, Reports.Insert(t.Context(), Report{ID: "open", Email: "a@x.dev"}))
	clk.Advance(1000 * 24 * time.Hour)
	time.Sleep(50 * time.Millisecond)
	if l := retentionLoop(t, app, "desk/store/reports retention"); l.Runs != 0 || l.NextRun != nil {
		t.Errorf("an idle retention: %+v", l)
	}
}

// A hold and a release on a due record: the retention leaves it, then
// takes it up.
func TestRetentionHonoursHolds(t *testing.T) {
	clk := clock.NewManualClock(epoch)
	app := startPrivacy(t, kit.Clock(clk))
	ctx := t.Context()
	must(t, Reports.Insert(ctx, Report{ID: "closed", Email: "b@x.dev", Name: "B", ClosedAt: at(0)}))
	armed(t, clk, 1)
	must(t, Reports.Hold(ctx, "closed", "litigation"))
	clk.Advance(100 * 24 * time.Hour)
	eventually(t, "the loop awake at the instant, the held record off its agenda", func() bool {
		return retentionLoop(t, app, "desk/store/reports retention").NextRun == nil
	})
	if r, err := Reports.Get(ctx, "closed"); err != nil || r.Name != "B" {
		t.Fatal("a held record was erased")
	}
	must(t, Reports.Release(ctx, "closed"))
	armed(t, clk, 1)
	clk.Advance(2 * time.Second)
	eventually(t, "the erasure after the release", func() bool {
		r, rErr := Reports.Get(ctx, "closed")
		if rErr != nil {
			t.Fatal(rErr)
		}
		return r.Name == ""
	})
}

// dry-run journals what the retention would do, and changes nothing.
func TestRetentionDryRunChangesNothing(t *testing.T) {
	t.Setenv("KIT_RETENTION", "dry-run")
	clk := clock.NewManualClock(epoch)
	app := startPrivacy(t, kit.Clock(clk))
	ctx := t.Context()
	must(t, Reports.Insert(ctx, Report{ID: "closed", Email: "b@x.dev", Name: "B", ClosedAt: at(0)}))
	armed(t, clk, 1)
	clk.Advance(100 * 24 * time.Hour)
	eventually(t, "the dry run's entry", func() bool {
		for _, e := range privacyOf(t, app).Journal {
			if e.DryRun && e.Op == model.JournalErase {
				return true
			}
		}
		return false
	})
	if r, err := Reports.Get(ctx, "closed"); err != nil || r.Name != "B" {
		t.Error("a dry run erased")
	}
}

// off: no retention runs, and a start outside dev says so.
func TestRetentionOffIsWarned(t *testing.T) {
	t.Setenv("KIT_RETENTION", "off")
	app := startPrivacy(t, kit.Env(kit.EnvProduction))
	found := false
	for _, d := range app.Graph().Diagnostics {
		found = found || (d.Severity == "warning" && strings.Contains(d.Message, "KIT_RETENTION=off"))
	}
	if !found {
		t.Error("KIT_RETENTION=off is not warned of")
	}
	for _, l := range app.Graph().Runtime.Loops {
		if l.Kind == model.LoopRetention {
			t.Errorf("a retention loop runs under off: %+v", l)
		}
	}
}
