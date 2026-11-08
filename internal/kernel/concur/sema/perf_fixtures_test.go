//go:build !race

// The fixture of sema's performance contract (design/sdk.yaml, budgets): it
// sets one call up and returns it, and the gen_perf_test.go kit gen writes
// beside this file counts its allocations against its budget. Race off.
package sema_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/sema"
)

// perfErr keeps each call's result alive.
var perfErr error

// perfAcquireRelease is Acquire's fixture: an uncontended Acquire and its
// Release, the path every admitted call takes.
func perfAcquireRelease(testing.TB) func() {
	s := sema.NewWeighted(64)
	ctx := context.Background()
	return func() {
		perfErr = s.Acquire(ctx, 1)
		s.Release(1)
	}
}
