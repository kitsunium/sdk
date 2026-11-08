//go:build unix

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The engine over real processes, on the default spawner (proc/exec): /bin/sh
// stands for the service.

// realUnit builds a unit over proc/exec and the system clock.
func realUnit(t *testing.T, cfg service.Config) *service.Unit {
	t.Helper()
	cfg.Name = "real"
	u, err := service.NewUnit(&cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = u.Close(ctx)
	})
	return u
}

// TestRealSimpleStartStop: a long-running shell is up once spawned, and a stop
// ends it with SIGTERM, a clean end for a daemon.
func TestRealSimpleStartStop(t *testing.T) {
	u := realUnit(t, service.Config{ExecStart: []coresvc.CommandValue{command(t, "/bin/sh", "-c", "exec sleep 30")}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st := u.State(); st.State != coresvc.StateActive || st.MainPID <= 0 {
		t.Fatalf("after Start: %+v", st)
	}
	if err := u.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if st := u.State(); st.State != coresvc.StateInactive || st.Result != coresvc.ResultSuccess {
		t.Fatalf("after Stop: %s %s", st.State, st.Result)
	}
}

// TestRealOneshotFailureAndStopPost: a oneshot that exits 3 fails its start
// with the result exit-code, and ExecStopPost= sees why.
func TestRealOneshotFailureAndStopPost(t *testing.T) {
	out := filepath.Join(t.TempDir(), "post")
	u := realUnit(t, service.Config{
		Type:         coresvc.TypeOneshot,
		ExecStart:    []coresvc.CommandValue{command(t, "/bin/sh", "-c", "exit 3")},
		ExecStopPost: []coresvc.CommandValue{command(t, ":/bin/sh", "-c", `echo "$SERVICE_RESULT $EXIT_CODE $EXIT_STATUS" > "$0"`, out)},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := u.Start(ctx)
	if !errs.HasCode(err, coresvc.CodeStartFailed) {
		t.Fatalf("Start: %v", err)
	}
	got, rerr := os.ReadFile(out)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.TrimSpace(string(got)) != "exit-code exited 3" {
		t.Fatalf("ExecStopPost= saw %q", got)
	}
}

// TestRealRestartUntilTheLimit: a crashing service is restarted until the
// start limiter refuses it.
func TestRealRestartUntilTheLimit(t *testing.T) {
	u := realUnit(t, service.Config{
		Restart: coresvc.RestartOnFailure, RestartDelay: 10 * time.Millisecond,
		StartLimitBurst: 3, StartLimitInterval: time.Minute,
		ExecStart: []coresvc.CommandValue{command(t, "/bin/sh", "-c", "exit 1")},
	})
	sub, _ := u.Subscribe(256)
	if err := u.Start(context.Background()); err != nil && !errs.HasCode(err, coresvc.CodeStartFailed) {
		t.Fatalf("Start: %v", err)
	}
	for {
		ev := waitState(t, sub, coresvc.StateFailed)
		if ev.Result == coresvc.ResultStartLimitHit {
			break
		}
	}
	if st := u.State(); st.Restarts != 3 {
		t.Fatalf("%d restarts  (the refused one included), want 3", st.Restarts)
	}
}
