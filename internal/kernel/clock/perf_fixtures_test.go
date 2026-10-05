//go:build !race

// The fixtures of clock's performance contracts (design/sdk.yaml, budgets):
// each sets one call up and returns it, and the perf_gen_test.go kit gen
// writes beside this file counts its allocations against its budget.
//
// They were the clock half of the kernel's zero-allocation gate
// (internal/kernel/zeroalloc_gate_integration_test.go): the per-record
// wall-clock read and the elapsed-duration read through System, the Timed
// every consumer defaults to, bound 0. The budget names the port's method
// (Clock.Now, Clock.Since), since System's concrete type is unexported: what
// is held is a call through the port, as every consumer makes it. Race off.
package clock_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// The sinks keep each call's result alive.
var (
	perfTime     time.Time
	perfDuration time.Duration
)

// perfNow is Clock.Now's fixture: System's wall-clock read.
func perfNow(testing.TB) func() {
	return func() { perfTime = clock.System.Now() }
}

// perfSince is Clock.Since's fixture: System's elapsed duration against a
// fixed start.
func perfSince(testing.TB) func() {
	start := clock.System.Now()
	return func() { perfDuration = clock.System.Since(start) }
}
