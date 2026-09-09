package lifecycle_test

import (
	"context"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svclc "github.com/kitsunium/sdk/internal/service/lifecycle"
)

// TestRunStartsWaitsAndStopsWithNothingWiredIn. The zero RunConfig installs no
// signal handler and sends no datagram: a library that arms process-wide
// machinery because it was imported fights the caller's own main.
func TestRunStartsWaitsAndStopsWithNothingWiredIn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		signals []coreproc.Signal
	}{
		//: the zero value, which is what a caller who never read the field
		//: passes, and the empty-but-present slice, which is what a config
		//: file with an empty list decodes to. Both must install nothing.
		{"a nil signal set", nil},
		{"an empty signal set", []coreproc.Signal{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := newRecorder()
			up := make(chan struct{})
			lc := svclc.New(svclc.Config{Clock: manual()})
			add(t, lc, rec.ok("db"), rec.signalling("http", up))
			ctx, cancel := context.WithCancel(context.Background())

			returned := runAsync(ctx, lc, svclc.RunConfig{Signals: tc.signals})
			//: rendezvous on the last component being up, not on a duration.
			//: Cancelling before Run reaches its select is harmless: a context
			//: that is already done simply wins the select on entry.
			<-up
			cancel()

			if err := <-returned; err != nil {
				t.Fatalf("Run: %v", err)
			}
			assertCalls(t, rec.snapshot(), []string{
				"start:db", "start:http", "stop:http", "stop:db",
			})
		})
	}
}

// TestRunReportsAFailedStartWithoutTryingToCleanUpTwice. Start has already
// unwound whatever it brought up by the time it returns, so Run has nothing
// left to do — and a second sweep would take a second, unrecorded pass at
// components that are already down.
func TestRunReportsAFailedStartWithoutTryingToCleanUpTwice(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc, rec.ok("db"), rec.failing("http", errAddrInUse))

	err := svclc.Run(context.Background(), lc, svclc.RunConfig{})
	assertHasCode(t, err, svclc.CodeStartFailed, "a failed start under Run")
	assertCalls(t, rec.snapshot(), []string{"start:db", "start:http", "stop:db"})
}

// TestAnUndeliverableReadinessTakesTheComponentsBackDown. A readiness that
// cannot be delivered is not cosmetic: the supervisor kills a unit that never
// reports ready, so a process left running with its components up is a process
// about to be killed with them still up.
//
// It is not t.Parallel: t.Setenv forbids it, and NOTIFY_SOCKET is
// process-wide. The env var is what makes sdnotify attempt a datagram at all —
// unset, every notifier call is a documented no-op that returns nil.
func TestAnUndeliverableReadinessTakesTheComponentsBackDown(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "/nonexistent/kitsunium-sdk-lifecycle/notify.sock")
	rec := newRecorder()
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc, rec.ok("db"), rec.ok("http"))

	err := svclc.Run(context.Background(), lc, svclc.RunConfig{Notify: true})
	assertHasCode(t, err, svclc.CodeReadinessFailed, "an undeliverable READY=1")
	assertCalls(t, rec.snapshot(), []string{
		"start:db", "start:http", "stop:http", "stop:db",
	})
}
