//go:build !race

// The fixture of singleflight's performance contract (design/sdk.yaml,
// budgets): it sets one call up and returns it, and the gen_perf_test.go kit
// gen writes beside this file counts its allocations against its budget. Race
// off.
package singleflight_test

import (
	"context"
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/singleflight"
)

// perfValue and perfErr keep each call's result alive.
var (
	perfValue int
	perfErr   error
)

// perfDoLeading is Do's fixture: a call that leads, on a key nobody else is
// asking for — the call record, its done channel, the shared context and the
// goroutine fn runs on, every time (#813). fn is built once, as a caller's is.
//
// The yield after each call lets the goroutine fn ran on finish exiting
// before the next call starts its own, so the runtime hands the next go
// statement that goroutine back instead of allocating a descriptor — the
// scheduler's allocation, not Do's. Without it, one fresh test process in
// three counted one to three 480-byte goroutine descriptors over the budget
// (20 of 60); with it, none did (0 of 200).
func perfDoLeading(testing.TB) func() {
	var group singleflight.Group[string, int]
	ctx := context.Background()
	fn := func(context.Context) (int, error) { return 1, nil }
	return func() {
		perfValue, _, perfErr = group.Do(ctx, "k", fn)
		runtime.Gosched()
	}
}
