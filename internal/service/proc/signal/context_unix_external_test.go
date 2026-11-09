//go:build unix

package signal_test

import (
	"context"
	"runtime"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/service/proc/signal"
)

// TestNotifyContext pins #1096's NotifyContext: a SIGUSR1 this test sends to
// its own process after the call returns cancels the context with the signal
// as its cause, and stop leaves no goroutine behind. Not parallel: the signal
// is process-wide.
func TestNotifyContext(t *testing.T) {
	//: os/signal starts its process-wide watcher at the first subscription and
	//: keeps it; start it before counting.
	_, warm := signal.NotifyContext(context.Background(), coreproc.Signal(syscall.SIGUSR2))
	warm()
	time.Sleep(10 * time.Millisecond)
	before := runtime.NumGoroutine()
	usr1 := coreproc.Signal(syscall.SIGUSR1)
	ctx, stop := signal.NotifyContext(context.Background(), usr1)
	//: this test's own process, with a signal it is subscribed to.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("kill(self, SIGUSR1): %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the context was not cancelled by SIGUSR1")
	}
	//: the cause is the signal, comparable and readable.
	if context.Cause(ctx) != (signal.ReceivedError{Signal: usr1}) {
		t.Fatalf("Cause = %v, want ReceivedError{SIGUSR1}", context.Cause(ctx))
	}
	if sig, ok := signal.CauseSignal(ctx); !ok || sig != usr1 {
		t.Fatalf("CauseSignal = %v %v, want SIGUSR1", sig, ok)
	}
	stop()
	stop()
	deadline := time.Now().Add(5 * time.Second)
	//: the goroutines end just after stop: wait for the count, bounded.
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("%d goroutines after stop, %d before", after, before)
	}
}

// TestNotifyContextStopWithoutSignal pins that stop cancels the context
// without a signal cause.
func TestNotifyContextStopWithoutSignal(t *testing.T) {
	ctx, stop := signal.NotifyContext(context.Background(), coreproc.Signal(syscall.SIGUSR2))
	stop()
	<-ctx.Done()
	//: no signal arrived.
	if _, ok := signal.CauseSignal(ctx); ok {
		t.Fatalf("CauseSignal after stop = %v, want none", context.Cause(ctx))
	}
}
