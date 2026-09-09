package group_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/group"
)

// noop is the task every benchmark below runs. It returns immediately on
// purpose: what is being measured is the PRIMITIVE, and any work inside would
// hide it.
func noop(context.Context) error { return nil }

// BenchmarkGoWait_OneTask is the floor: one submission, one wait. Everything a
// group costs is in here — a context derivation, a semaphore round trip, a
// goroutine, and a WaitGroup park/unpark.
func BenchmarkGoWait_OneTask(b *testing.B) {
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		g, _ := group.New(ctx, group.Unlimited)
		g.Go(noop)
		if err := g.Wait(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGoWait_EightTasks_Unlimited is the shape a caller actually writes:
// a fan-out with no bound. The per-task cost is this divided by eight, and it
// is the number to compare against the direct-call baseline.
func BenchmarkGoWait_EightTasks_Unlimited(b *testing.B) {
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		g, _ := group.New(ctx, group.Unlimited)
		for range 8 {
			g.Go(noop)
		}
		if err := g.Wait(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGoWait_EightTasks_Limit2 is the same fan-out through a semaphore
// that actually refuses. The gap against the unlimited case is what a bound
// costs when it binds — the submitting goroutine parks waiting for a slot.
func BenchmarkGoWait_EightTasks_Limit2(b *testing.B) {
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		g, _ := group.New(ctx, 2)
		for range 8 {
			g.Go(noop)
		}
		if err := g.Wait(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGoWait_EightTasks_Serial is the clamp ADR 0031 applies to a
// non-positive limit. It is here so the cost of the clamp is a published
// number rather than an assumption: a caller who forgot the limit gets THIS,
// not a deadlock.
func BenchmarkGoWait_EightTasks_Serial(b *testing.B) {
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		g, _ := group.New(ctx, 0)
		for range 8 {
			g.Go(noop)
		}
		if err := g.Wait(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCollect_EightTasks measures the typed fan-out. The delta against
// BenchmarkGoWait_EightTasks_Unlimited is the result slice and the closures —
// everything else is the same Group.
func BenchmarkCollect_EightTasks(b *testing.B) {
	ctx := b.Context()
	fns := make([]func(context.Context) (int, error), 8)
	for i := range fns {
		fns[i] = func(context.Context) (int, error) { return i, nil }
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := group.Collect(ctx, group.Unlimited, fns); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBaseline_EightDirectCalls runs the same eight tasks with no group at
// all. Every number above is only meaningful against it: concurrency is worth
// its overhead exactly when the tasks cost more than this difference.
func BenchmarkBaseline_EightDirectCalls(b *testing.B) {
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for range 8 {
			if err := noop(ctx); err != nil {
				b.Fatal(err)
			}
		}
	}
}
