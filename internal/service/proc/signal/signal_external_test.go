//go:build unix

// Package signal_test — black-box acceptance tests for Notify: a self-sent
// signal must arrive typed, and the teardown must be complete and idempotent.
package signal_test

import (
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcsignal "github.com/kitsunium/sdk/internal/service/proc/signal"
)

// deliveryTimeout bounds every wait on a signal that must already be in flight.
const deliveryTimeout time.Duration = 10 * time.Second

// TestNotify asserts a self-sent signal is delivered as a typed value, that the
// stop function tears the subscription down completely, and that calling it
// again is a safe no-op.
//
// The closed channel is what proves the teardown: translate closes it from its
// own defer, so observing the close IS observing the goroutine return. That is a
// deterministic signal, unlike a runtime.NumGoroutine comparison, which cannot
// tell this package's goroutine from anyone else's.
func TestNotify(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		sig  syscall.Signal
		//: how many times to send before tearing down; a burst must not be
		//: dropped, which is what the per-signal buffer slot is for.
		sends int
	}
	//: a distinct signal per case. A self-kill is process-wide, so two cases
	//: subscribed to the SAME signal would each see the other's deliveries and
	//: the burst assertion would stop meaning anything.
	tests := []tc{
		{"a single SIGUSR1", syscall.SIGUSR1, 1},
		{"a single SIGWINCH", syscall.SIGWINCH, 1},
		{"a burst of SIGUSR2", syscall.SIGUSR2, 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		want := coreproc.Signal(c.sig)
		ch, stop := svcsignal.Notify(want)

		for range c.sends {
			//: Notify has already registered by the time it returns, so a
			//: self-sent signal here cannot be missed.
			if err := syscall.Kill(syscall.Getpid(), c.sig); err != nil {
				t.Fatalf("self-kill %v: %v", c.sig, err)
			}
			select {
			case got := <-ch:
				//: what we sent is what we receive, typed.
				if got != want {
					t.Fatalf("delivered %v, want %v", got, want)
				}
			case <-time.After(deliveryTimeout):
				t.Fatalf("timed out waiting for %v", c.sig)
			}
		}

		stop()

		//: stop closes the channel, and the close only happens in the
		//: translator's defer — so a closed channel means the goroutine
		//: returned and the os/signal registration is gone with it.
		drainUntilClosed(t, ch)

		//: a second stop must be a safe no-op, never a double-close panic.
		stop()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestNotifyStopConcurrent asserts the stop function is safe under concurrent
// invocation: many goroutines calling it at once must not double-close the
// channel (which would panic) nor race — proving the sync.Once guard. Under
// -race this fails loudly if the guard regresses to an unsynchronised bool.
func TestNotifyStopConcurrent(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		callers int
	}
	tests := []tc{
		{"two callers", 2},
		{"thirty-two callers", 32},
		{"a hundred callers", 100},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ch, stop := svcsignal.Notify(coreproc.Signal(syscall.SIGCONT))

		done := make(chan struct{}, c.callers)
		//: launch many concurrent stop callers racing on the same closure.
		for range c.callers {
			go func() {
				stop()
				done <- struct{}{}
			}()
		}
		for range c.callers {
			select {
			case <-done:
			case <-time.After(deliveryTimeout):
				t.Fatal("a concurrent stop caller never returned")
			}
		}

		//: exactly one close must have happened, and it must have happened —
		//: the channel is closed, and reading it again does not panic.
		drainUntilClosed(t, ch)

		//: a further call after the storm must still be a safe no-op.
		stop()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// drainUntilClosed reads ch until it is closed, failing if that never happens.
//
// Draining rather than asserting on the first read is deliberate: a self-kill is
// process-wide, so a subscription can hold a signal another test sent. What is
// being pinned here is that the channel CLOSES — which only happens in the
// translator's defer, and therefore proves the goroutine returned.
func drainUntilClosed(t *testing.T, ch <-chan coreproc.Signal) {
	t.Helper()
	deadline := time.After(deliveryTimeout)
	for {
		select {
		case _, ok := <-ch:
			//: a closed channel is the end of the stream and of the goroutine.
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the channel was never closed after stop")
			return
		}
	}
}
