//go:build unix

package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/framework/telemetry"
	"github.com/kitsunium/sdk/pkg/v1/proc/ipc"
)

// A supervised loop's restarts are counted on the telemetry port — one
// restart record each, its node, its failure's code and the wait before it
// runs again — and its giving up is one record more: a collector counts a
// node's restarts and learns why the product stopped being live.
func TestASupervisedLoopReportsItsRestartsAndItsGivingUp(t *testing.T) {
	telPath := filepath.Join(shortDir(t), "run", "t.sock")
	var fail atomic.Bool
	svc := app.NewService("ticker", "A loop whose runs fail when told.")
	tick := svc.Loop("tick", func(context.Context, app.WakeEvent) error {
		if fail.Load() {
			return app.Unavailable("the test's failure")
		}
		return nil
	}, app.WakeEvery(time.Hour), app.GiveUpAfter(1, time.Hour))
	a := app.NewApp("statusline", svc).With(app.Profile(model.ProfileDaemon), app.InMemory(), app.Logs(io.Discard),
		app.Env(app.EnvProduction), app.Telemetry(telPath))
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := a.Stop(context.Background()); err != nil {
			t.Errorf("stop: %v", err)
		}
	}()
	tc, err := ipc.Dial(t.Context(), ipc.Config{Path: telPath})
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	r := bufio.NewReader(tc)
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var h struct{ Nodes []string }
	if err := json.Unmarshal(line, &h); err != nil {
		t.Fatalf("the handshake: %s %v", line, err)
	}
	ref := telemetry.NodeRef(slices.Index(h.Nodes, "ticker/loop/tick") + 1)
	if ref == 0 {
		t.Fatalf("the node table lacks the loop: %v", h.Nodes)
	}
	if err := tc.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	next := func(kind telemetry.Kind) telemetry.Event {
		t.Helper()
		b := make([]byte, telemetry.RecordSize)
		for {
			if _, err := io.ReadFull(r, b); err != nil {
				t.Fatalf("no record of kind %d: %v", kind, err)
			}
			if ev, _, ok := telemetry.Decode(b); ok && ev.Kind == kind && ev.Node == ref {
				return ev
			}
		}
	}
	fail.Store(true)
	tick.Nudge()
	restart := next(telemetry.KindRestart)
	if restart.Outcome != telemetry.OutcomeError || restart.Op != telemetry.OpRun || restart.Duration != int64(time.Second) {
		t.Errorf("the restart's record: %+v; want a failed run, a second's wait", restart)
	}
	tick.Nudge()
	gaveUp := next(telemetry.KindGaveUp)
	if gaveUp.Outcome != telemetry.OutcomeError || gaveUp.Code != uint32(app.CodeGaveUp) {
		t.Errorf("the giving up's record: %+v; want CodeGaveUp", gaveUp)
	}
}
