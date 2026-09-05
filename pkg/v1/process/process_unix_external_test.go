//go:build unix

// Package process_test — the live spawn. Start/Stop/Wait need a real child, so
// the round trip is gated on the same `unix` tag as the implementation rather
// than on a runtime.GOOS guess; the non-Unix contract lives in
// process_other_test.go.
package process_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/process"
)

// shPath is the shell used by the live-spawn facade test. POSIX requires it at
// this path, so an absent /bin/sh is a broken host rather than a reason to
// silently pass.
const shPath = "/bin/sh"

// TestStartAndStop exercises the full facade path: spawn, group-stop, and Wait.
func TestStartAndStop(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		script  string
		setpgid bool
		signal  process.Signal
	}
	tests := []tc{
		{
			// A shell that backgrounds and waits is the case Setpgid exists
			// for: signalling the leader alone would leave the sleep running.
			"a process group stopped by SIGTERM",
			"sleep 30 & wait", true, process.SIGTERM,
		},
		{"a single process stopped by SIGTERM", "sleep 30", false, process.SIGTERM},
		{
			// SIGKILL cannot be caught, so it exercises the path where the
			// grace window never matters.
			"a process group killed outright",
			"sleep 30 & wait", true, process.SIGKILL,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p, err := process.Start(t.Context(), process.Spec{
			Path:    shPath,
			Args:    []string{"sh", "-c", c.script},
			Setpgid: c.setpgid,
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		//: the handle must report a positive leader pid.
		if p.PID() <= 0 {
			t.Fatalf("PID() = %d, want > 0", p.PID())
		}
		//: a graceful group stop must succeed within the grace window.
		if sErr := p.Stop(t.Context(), 2*time.Second, c.signal); sErr != nil {
			t.Fatalf("Stop: %v", sErr)
		}
		//: Wait must reap the stopped group without a host fault.
		if _, wErr := p.Wait(); wErr != nil {
			t.Fatalf("Wait: %v", wErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
