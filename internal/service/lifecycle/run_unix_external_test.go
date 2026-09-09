//go:build unix

// Package lifecycle_test — the half of Run that needs a real OS signal.
//
// It is constrained to unix because raising a signal at one's own process is:
// syscall.Kill has no Windows equivalent, and internal/service/proc/signal
// draws the same line for the same reason (ADR 0018 — the runtime bar is met
// where the mechanism exists, not pretended elsewhere). On Windows the wiring
// is still COMPILED, and Run's context path is covered by the portable file
// next to this one.
package lifecycle_test

import (
	"context"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svclc "github.com/kitsunium/sdk/internal/service/lifecycle"
)

// TestRunStopsWhenASubscribedSignalArrives is the whole point of the opt-in
// field: a supervisor's SIGTERM has to reach the shutdown, and it has to do it
// through the proc domain the SDK already ships rather than through a second
// signal handler written here.
//
// SIGUSR1 stands in for SIGTERM so a failure cannot take the test binary down
// with it, and the test is not parallel because signal disposition is
// process-wide.
func TestRunStopsWhenASubscribedSignalArrives(t *testing.T) {
	usr1 := coreproc.Signal(syscall.SIGUSR1)
	//: arm the process disposition BEFORE anything is raised. Run installs its
	//: own subscription only after the last Start returns, and until SOME
	//: subscription exists SIGUSR1's default action is to terminate — so a
	//: raise arriving one instruction early would kill the test binary rather
	//: than fail it. The guard goes through the STDLIB deliberately: routing
	//: it through the SDK's own signal package would make the safety net and
	//: the code under test the same mechanism.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGUSR1)
	defer signal.Stop(guard)

	rec := newRecorder()
	up := make(chan struct{})
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc, rec.ok("db"), rec.signalling("http", up))

	returned := runAsync(context.Background(), lc, svclc.RunConfig{Signals: []coreproc.Signal{usr1}})
	//: every component is up, so Run is on its way to its select. The context
	//: here is deliberately never cancelled: the signal is the ONLY thing that
	//: can end this wait, which is exactly what is being asserted.
	<-up

	//: raise until it lands. Run subscribes after the last Start returns, so
	//: the first raise may precede its subscription; retrying closes that
	//: window without a sleep and without a tolerance. Gosched yields the
	//: processor without consulting any clock.
	for {
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR1); err != nil {
			t.Fatalf("raising SIGUSR1: %v", err)
		}
		select {
		case err := <-returned:
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			assertCalls(t, rec.snapshot(), []string{
				"start:db", "start:http", "stop:http", "stop:db",
			})
			return
		case <-guard:
			//: drained so the guard channel cannot fill and start dropping,
			//: which would leave this loop raising forever.
			runtime.Gosched()
		default:
			runtime.Gosched()
		}
	}
}
