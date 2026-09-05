//go:build unix

// Package signal_test — black-box tests for the Unix Relay: what it delivers,
// and what it refuses to deliver.
package signal_test

import (
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsignal "github.com/kitsunium/sdk/internal/service/proc/signal"
)

// TestRelay asserts the delivery contract in both directions: a signal reaches
// a live process group through a negative target, and a target Relay must not
// hand to kill(2) is refused before anything is delivered.
//
// The reserved targets are the reason this guard exists at all. kill(2) reads 0
// as "the caller's own process group" and -1 as "every process the caller may
// signal", so a zero-value Target — the shape a struct field gets when nobody
// sets it — would fan a SIGTERM across the whole session.
func TestRelay(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: a nil target function means the case builds no child and asserts a
		//: refusal against the fixed target below.
		target   func(t *testing.T) svcsignal.Target
		fixed    svcsignal.Target
		wantErr  bool
		wantKill bool
	}
	tests := []tc{
		{
			name:     "a live process group",
			target:   startIsolatedChild,
			wantKill: true,
		},
		{"the caller's own process group", nil, 0, true, false},
		{"every process the caller may signal", nil, -1, true, false},
		//: an impossible pid: kill(2) reports ESRCH, which Relay must surface
		//: typed rather than as a bare errno.
		{"a pid that cannot exist", nil, svcsignal.Target(0x7fffffff), true, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		target := c.fixed
		var waitErr chan error
		if c.target != nil {
			target = c.target(t)
			waitErr = childWait(t, int(-target))
		}

		//: one SIGTERM, then close so Relay drains and returns.
		src := make(chan coreproc.Signal, 1)
		src <- coreproc.Signal(syscall.SIGTERM)
		close(src)

		err := svcsignal.Relay(src, target)

		if c.wantErr {
			//: every refusal and every delivery failure carries one code, so a
			//: supervisor can classify it without matching on text.
			if !errs.HasCode(err, coreproc.CodeRelayFailed) {
				t.Fatalf("Relay(target=%d) = %v, want RELAY_FAILED", target, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Relay(target=%d) = %v, want nil", target, err)
		}
		if !c.wantKill {
			return
		}

		//: the signalled child must exit promptly; a timeout means the group
		//: never received anything.
		select {
		case werr := <-waitErr:
			assertKilledBySIGTERM(t, werr)
		case <-time.After(15 * time.Second):
			t.Fatal("the child did not exit after the group-relayed SIGTERM")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// startIsolatedChild forks a sleeping child into its OWN process group and
// returns the negative target addressing that group. Its own group is essential:
// a group-wide kill(2) against the test runner's group would take out sibling
// processes, including the runner itself.
func startIsolatedChild(t *testing.T) svcsignal.Target {
	t.Helper()
	//: POSIX mandates sleep; an absent one is a broken host, not a reason to
	//: quietly pass.
	path, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("resolving sleep: %v", err)
	}
	cmd := exec.Command(path, "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if serr := cmd.Start(); serr != nil {
		t.Fatalf("starting the child: %v", serr)
	}
	t.Cleanup(func() {
		//: a Kill error means the child already exited — both outcomes are fine.
		if kerr := cmd.Process.Kill(); kerr != nil {
			t.Logf("cleanup Kill: %v (the child had likely already exited)", kerr)
		}
	})
	//: with Setpgid and a zero Pgid the child's pid IS its group id.
	registerChild(t, cmd)
	return svcsignal.Target(-cmd.Process.Pid)
}

// children maps a child's process-group id to the command that owns it, so the
// single Wait owner can be started once the target is known.
var (
	children   = map[int]*exec.Cmd{}
	childrenMu sync.Mutex
)

// registerChild records cmd under its process-group id for childWait.
func registerChild(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	childrenMu.Lock()
	defer childrenMu.Unlock()
	children[cmd.Process.Pid] = cmd
}

// childWait starts the single Wait owner for the child in group pgid and
// returns the channel it reports on.
//
// Goroutine lifecycle: exactly one goroutine runs cmd.Wait. It reports on a
// buffered channel so it can never block on send, and the child is force-killed
// by the t.Cleanup above on every exit path, so the goroutine always joins.
func childWait(t *testing.T, pgid int) chan error {
	t.Helper()
	childrenMu.Lock()
	cmd := children[pgid]
	childrenMu.Unlock()
	if cmd == nil {
		t.Fatalf("no child registered for group %d", pgid)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	return waitErr
}

// assertKilledBySIGTERM checks the child died by exactly the relayed signal
// rather than by exiting on its own or by some other signal.
func assertKilledBySIGTERM(t *testing.T, err error) {
	t.Helper()
	//: a process killed by a signal yields an *exec.ExitError.
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("the child's Wait = %v, want an *exec.ExitError from SIGTERM", err)
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("the wait status is %T, want syscall.WaitStatus", exitErr.Sys())
	}
	if !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Fatalf("the child died by %v (signaled=%v), want SIGTERM", ws.Signal(), ws.Signaled())
	}
}
