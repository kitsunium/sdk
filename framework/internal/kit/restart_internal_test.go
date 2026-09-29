package kit

import (
	"context"
	"io"
	"testing"
	"time"
)

// A graph notice armed in one run must not touch the next one: Start
// replaces the hub the notice publishes on. The hook makes the notice slow
// enough to still be running when the app stops and starts again; the race
// detector does the rest.
func TestGraphNoticeDoesNotOutliveItsRun(t *testing.T) {
	graphNoticeHook = func() { time.Sleep(100 * time.Millisecond) }
	t.Cleanup(func() { graphNoticeHook = func() {} })
	svc := NewService("restart-probe", "A service for the restart test.")
	app := NewApp("restart", svc).With(InMemory(), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	for range 3 {
		if err := app.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		app.graphChanged()
		time.Sleep(270 * time.Millisecond) // the notice has fired and sits in the hook
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := app.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		cancel()
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
