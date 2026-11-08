//go:build !windows

package service

import (
	"testing"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// The replay of an instance's end (S0f, kitsunium/sdk#740): an instance ends
// with the first run that is over, unless a restart is due or a start queued.

// instance is a harness over an instance's settings.
func instance(t *testing.T, cfg Config) *harness {
	t.Helper()
	h := newHarness(t, cfg)
	h.m.s.put(setInstance, true)
	return h
}

// TestInstanceEndsWithItsRun: an instance is not ended before it ran; it
// ends once its main process is gone, once a failed start is over, and — with
// Restart= — only once no restart is due.
func TestInstanceEndsWithItsRun(t *testing.T) {
	start := []coresvc.CommandValue{cmd(t, "/bin/d")}
	h := instance(t, Config{ExecStart: start})
	h.request(evResetFailed)
	if h.exited {
		t.Fatal("an instance that never ran ended")
	}
	h.wantReply(h.start(), 0)
	h.exit(h.m.mainPID, 0)
	h.want(coresvc.StateInactive, coresvc.SubDead)
	if !h.exited {
		t.Fatal("an instance whose process ended did not end")
	}

	f := instance(t, Config{ExecStart: start})
	f.failSpawn[listStart] = true
	f.wantReply(f.start(), coresvc.CodeStartFailed)
	if !f.exited || f.m.result != coresvc.ResultResources {
		t.Fatalf("a failed start: ended %v, result %s", f.exited, f.m.result)
	}

	r := instance(t, Config{ExecStart: start, Restart: coresvc.RestartOnFailure})
	r.start()
	r.exit(r.m.mainPID, 1)
	r.want(coresvc.StateAutoRestart, coresvc.SubAutoRestart)
	if r.exited {
		t.Fatal("an instance ended with a restart due")
	}
	r.advance(r.deadline(timerState))
	r.want(coresvc.StateActive, coresvc.SubRunning)
	r.stopAndExit()
	if !r.exited {
		t.Fatal("a stopped instance did not end")
	}
}
