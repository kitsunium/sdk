package kit

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// stoppable declares a mailer whose outbox the test stops behind kit's back.
var stoppable = func() *Mailer {
	s := NewService("stoppable", "A mailer whose outbox stops, for the connectors' tests.")
	return s.Mailer("mail")
}()

// An outbox that stops while the product still serves says so at its next
// look — a publication, or the poll that bounds what another process
// publishes: its loop reads stopped with kit's words for why, and the live
// stream carries it — the Studio's connector turns "en échec" from that, not
// from a guess. The test drives the poll with the app's clock.
func TestAStoppedOutboxSaysSo(t *testing.T) {
	clk := clock.NewManualClock(time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC))
	app := NewApp("stops", stoppable.svc).With(DataDir(t.TempDir()), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard), Clock(clk))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	stoppable.mu.Lock()
	spool := stoppable.spool
	stoppable.mu.Unlock()
	if err := spool.Close(); err != nil {
		t.Fatalf("close the outbox: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, l := range app.sortedLoops() {
			if l.Node == stoppable.id && l.State == model.LoopStopped {
				if l.LastError == "" || l.Errors == 0 {
					t.Fatalf("the stopped outbox says nothing: %+v", l)
				}
				if app.currentPhase() != model.PhaseServing {
					t.Fatalf("the product stopped serving: %s", app.currentPhase())
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the outbox's loop never said it stopped: %+v", app.sortedLoops())
		}
		clk.Advance(pollInterval)
		time.Sleep(10 * time.Millisecond)
	}
}
