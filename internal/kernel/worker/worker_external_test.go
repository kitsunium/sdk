package worker_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/worker"
)

// note: Every's tests live in every_external_test.go to satisfy the
// per-file source↔test correspondence (KTN-TEST-FILES).

// TestStart validates the constructor contract: a nil loop panics, and a
// spawned loop runs and closes Done when it returns on its own.
func TestStart(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		runner func(t *testing.T)
	}
	tests := []tc{
		{
			name: "nil loop panics",
			runner: func(t *testing.T) {
				//: a nil loop is a programmer error — Start must fail loud.
				defer func() {
					//: the deferred recover turns the expected panic into a pass.
					if recover() == nil {
						t.Error("Start(nil) did not panic")
					}
				}()
				worker.Start(nil)
			},
		},
		{
			name: "loop runs and Done closes when it returns",
			runner: func(t *testing.T) {
				var ran atomic.Bool
				//: a self-terminating loop returns immediately; Done must close.
				d := worker.Start(func(_ <-chan struct{}) { ran.Store(true) })
				select {
				case <-d.Done():
					//: loop returned — the join channel is closed as documented.
				case <-time.After(2 * time.Second):
					t.Fatal("Done did not close after the loop returned")
				}
				if !ran.Load() {
					t.Error("loop body did not run")
				}
			},
		},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; each runner owns its own recover where a panic is expected.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		tc.runner(t)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDaemon_StopJoinsLoop proves Stop signals the loop AND blocks until the
// loop has actually returned — the join semantics the async drainer relies on.
func TestDaemon_StopJoinsLoop(t *testing.T) {
	t.Parallel()
	type tc struct{ name string }
	tests := []tc{
		{"Stop closes stop and joins the loop"},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the loop must observe stop and set returned BEFORE Stop returns.
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		var returned atomic.Bool
		//: the loop blocks on stop, then records its return — Stop must not
		//: come back until that record is visible (true join, not fire-and-forget).
		d := worker.Start(func(stop <-chan struct{}) {
			<-stop
			returned.Store(true)
		})
		d.Stop()
		//: Stop joined the loop, so its post-stop write must be observable.
		if !returned.Load() {
			t.Error("Stop returned before the loop had returned; join failed")
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDaemon_StopIsIdempotent pins the documented contract that Stop is safe
// to call multiple times and concurrently without panicking on a double close.
func TestDaemon_StopIsIdempotent(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		callers int
	}
	tests := []tc{
		{"sequential double Stop never panics", 1},
		{"concurrent Stop callers never panic", 32},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; every Stop caller must return without a double-close panic.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		d := worker.Start(func(stop <-chan struct{}) { <-stop })
		//: first Stop joins the loop; the rest are no-op closes + closed-chan reads.
		d.Stop()
		var wg sync.WaitGroup
		for range tc.callers {
			wg.Go(func() {
				//: a second/concurrent Stop must be a safe no-op via sync.Once.
				d.Stop()
			})
		}
		wg.Wait()
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
