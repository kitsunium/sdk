//go:build unix

// Package signal_test — black-box acceptance tests for the service signal
// toolbox: Notify delivery + leak-free stop, and Relay to a process group.
package signal_test

import (
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsignal "github.com/kitsunium/sdk/internal/service/proc/signal"
)

// waitGoroutines polls runtime.NumGoroutine until it drops to at most want or the
// deadline elapses, returning the final count. It tolerates scheduler lag so the
// leak assertion is not flaky.
func waitGoroutines(want int) (got int) {
	deadline := time.Now().Add(2 * time.Second)
	//: poll until the count settles or the deadline passes.
	for {
		got = runtime.NumGoroutine()
		//: the translator has exited once the count is back to baseline.
		if got <= want {
			//: settled at or below baseline — no leak.
			return got
		}
		//: give up once the deadline elapses and report the last reading.
		if time.Now().After(deadline) {
			//: deadline hit — return whatever we last observed.
			return got
		}
		//: brief yield lets the translator goroutine finish unwinding.
		time.Sleep(5 * time.Millisecond)
	}
}

// settledGoroutines samples runtime.NumGoroutine until two consecutive readings
// agree, returning that stable count. It establishes a baseline AFTER transient
// warm-up goroutines have unwound so the leak assertion compares like for like.
func settledGoroutines() (stable int) {
	prev := runtime.NumGoroutine()
	deadline := time.Now().Add(2 * time.Second)
	//: poll until two back-to-back samples match (the count has stopped moving).
	for {
		//: let any in-flight unwind progress between samples.
		time.Sleep(10 * time.Millisecond)
		cur := runtime.NumGoroutine()
		//: two equal consecutive readings mean the count has settled.
		if cur == prev {
			//: stable — this is the baseline.
			return cur
		}
		//: bail out at the deadline with the latest reading rather than spin.
		if time.Now().After(deadline) {
			//: deadline hit — accept the most recent sample.
			return cur
		}
		prev = cur
	}
}

// TestNotifyDeliversThenStops asserts a self-sent signal is delivered as a typed
// value, then the stop func unregisters the subscription (no further delivery)
// and leaks no goroutine — the central #62 Notify contract.
func TestNotifyDeliversThenStops(t *testing.T) {
	//: not parallel: it measures the process-wide goroutine count.

	//: warm up os/signal first — its internal signal_recv goroutine starts on
	//: the first Notify ever and never exits; baseline must be taken AFTER it so
	//: that one-time runtime goroutine is not mistaken for a leak.
	_, warmStop := svcsignal.Notify(coreproc.Signal(syscall.SIGUSR1))
	warmStop()
	//: let the warm-up translator unwind, then sample a settled baseline.
	base := settledGoroutines()

	ch, stop := svcsignal.Notify(coreproc.Signal(syscall.SIGUSR1))

	//: deliver a signal to ourselves; Notify must surface it on the typed chan.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("self-kill SIGUSR1: %v", err)
	}

	//: the delivered value must be the typed SIGUSR1, not an os.Signal.
	select {
	case got := <-ch:
		//: identity check: what we sent is what we receive, typed.
		if got != coreproc.Signal(syscall.SIGUSR1) {
			t.Fatalf("delivered %v, want SIGUSR1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SIGUSR1 delivery")
	}

	stop()

	//: stop closes the channel; a closed channel reads the zero value, not a
	//: further signal — proving the subscription was torn down.
	select {
	case got, ok := <-ch:
		//: after stop the channel must be closed (ok == false).
		if ok {
			t.Fatalf("received %v after stop; want closed channel", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after stop")
	}

	//: stop must be idempotent: a second call is a safe no-op, never a panic.
	stop()

	//: the translation goroutine must be gone — no leak after teardown.
	if got := waitGoroutines(base); got > base {
		t.Fatalf("goroutine leak: have %d, baseline %d", got, base)
	}
}

// TestRelayToProcessGroup asserts Relay forwards a received signal to a process
// group when the target is negative (-pgid), the #62 Relay contract. The target
// group is a freshly-forked child in its OWN process group (Setpgid), so the
// group-wide kill(2) never reaches the test runner's own group.
//
// goroutine lifecycle: one goroutine runs cmd.Wait on the child and reports the
// result on a buffered channel; the test always receives that result (success
// path) or the deferred Kill+drain forces the child to exit and the goroutine to
// send, so the goroutine is always joined and never leaks.
func TestRelayToProcessGroup(t *testing.T) {
	t.Parallel()

	//: a long sleep that does nothing but wait to be signalled.
	cmd := exec.Command("sleep", "30")
	//: place the child in its own process group so -pgid targets ONLY it,
	//: never the test runner's group (which would kill sibling processes).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	//: a host without a usable sleep binary cannot exercise this path.
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start child: %v", err)
	}
	//: the child's pid doubles as its process-group id (Setpgid with Pgid 0).
	pgid := cmd.Process.Pid

	//: a single Wait owner: this goroutine reaps the child and reports how it died.
	waitErr := make(chan error, 1)
	//: Wait blocks until the signalled child exits; run it off the deadline path.
	go func() {
		//: capture the child's termination so we can inspect the signal.
		waitErr <- cmd.Wait()
	}()

	//: on any early failure, force the child dead so the Wait goroutine joins and
	//: no zombie or goroutine survives the test.
	defer func() {
		//: a Kill error means the child already exited — both outcomes are fine.
		if kerr := cmd.Process.Kill(); kerr != nil {
			t.Logf("cleanup Kill: %v (child likely already exited)", kerr)
		}
	}()

	//: feed Relay a single SIGTERM, then close so Relay drains and returns nil.
	src := make(chan coreproc.Signal, 1)
	src <- coreproc.Signal(syscall.SIGTERM)
	close(src)

	//: forward to -pgid — kill(2) delivers SIGTERM to every process in the
	//: child's isolated group (just the sleep), proving negative-target routing.
	if err := svcsignal.Relay(src, svcsignal.Target(-pgid)); err != nil {
		t.Fatalf("Relay to -pgid returned %v, want nil", err)
	}

	//: the signalled child must exit promptly; a timeout means delivery failed.
	select {
	case err := <-waitErr:
		//: a process killed by a signal yields an *exec.ExitError.
		exitErr, ok := err.(*exec.ExitError)
		//: SIGTERM is fatal by default, so Wait must report a non-nil error.
		if !ok {
			t.Fatalf("child Wait err = %v, want *exec.ExitError from SIGTERM", err)
		}
		ws, ok := exitErr.Sys().(syscall.WaitStatus)
		//: the wait status must expose how the child died.
		if !ok {
			t.Fatalf("wait status type %T, want syscall.WaitStatus", exitErr.Sys())
		}
		//: the child must have been killed by exactly the relayed SIGTERM.
		if !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
			t.Fatalf("child died by %v (signaled=%v), want SIGTERM", ws.Signal(), ws.Signaled())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not exit after group-relayed SIGTERM")
	}
}

// TestRelayFailsTyped asserts a delivery failure surfaces the typed RELAY_FAILED
// code rather than a bare stdlib error.
func TestRelayFailsTyped(t *testing.T) {
	t.Parallel()

	//: pid 0x7fffffff cannot exist; kill(2) fails with ESRCH, which Relay wraps.
	src := make(chan coreproc.Signal, 1)
	src <- coreproc.Signal(syscall.SIGTERM)
	close(src)

	//: target an impossible pid so the first delivery fails deterministically.
	err := svcsignal.Relay(src, svcsignal.Target(0x7fffffff))
	//: the failure must carry the central RELAY_FAILED code.
	if !errs.HasCode(err, coreproc.CodeRelayFailed) {
		t.Fatalf("Relay err = %v, want CodeRelayFailed", err)
	}
}
