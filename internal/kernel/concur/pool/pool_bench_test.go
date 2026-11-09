package pool_test

import (
	"context"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/group"
	"github.com/kitsunium/sdk/internal/kernel/concur/pool"
)

// BenchmarkSubmit_Steadystate is one Submit of a task that does nothing, on a
// pool of four workers with a queue of 1024, waiting when the queue is full:
// the price of handing a task over in steady state. The task is built once —
// the closure is the caller's —, so #804's acceptance reads 0 allocs/op.
func BenchmarkSubmit_Steadystate(b *testing.B) {
	p := pool.NewPool(context.Background(), pool.Config{Workers: 4, Queue: 1024})
	ctx := context.Background()
	task := func(context.Context) error { return nil }
	b.ReportAllocs()
	for b.Loop() {
		if err := p.Submit(ctx, task); err != nil {
			b.Fatalf("Submit: %v", err)
		}
	}
	b.StopTimer()
	if err := p.Drain(ctx); err != nil {
		b.Fatalf("Drain: %v", err)
	}
}

// BenchmarkSubmit_Parallel is Submit from every P at once into the same
// pool, run with -cpu 1,4,8.
func BenchmarkSubmit_Parallel(b *testing.B) {
	p := pool.NewPool(context.Background(), pool.Config{Workers: 4, Queue: 1024})
	ctx := context.Background()
	task := func(context.Context) error { return nil }
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := p.Submit(ctx, task); err != nil {
				b.Errorf("Submit: %v", err)
				return
			}
		}
	})
	b.StopTimer()
	if err := p.Drain(ctx); err != nil {
		b.Fatalf("Drain: %v", err)
	}
}

// BenchmarkDrain_1000 is a pool's whole life around a burst: New, 1000 tasks
// submitted, Drain — per op, the burst.
func BenchmarkDrain_1000(b *testing.B) {
	ctx := context.Background()
	task := func(context.Context) error { return nil }
	b.ReportAllocs()
	for b.Loop() {
		p := pool.NewPool(ctx, pool.Config{Workers: 4, Queue: 1024})
		for range 1000 {
			if err := p.Submit(ctx, task); err != nil {
				b.Fatalf("Submit: %v", err)
			}
		}
		if err := p.Drain(ctx); err != nil {
			b.Fatalf("Drain: %v", err)
		}
	}
}

// BenchmarkGroupGoWait_OneTask is the baseline #804 names: group's one-shot
// fan-out, a goroutine per task — New, one Go, Wait.
func BenchmarkGroupGoWait_OneTask(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		g, _ := group.New(ctx, 4)
		g.Go(func(context.Context) error { return nil })
		if err := g.Wait(); err != nil {
			b.Fatalf("Wait: %v", err)
		}
	}
}

// BenchmarkSubmitWait_OneTask is the same one task through a running pool,
// waited for: the per-task cost a caller that needs the task done pays.
func BenchmarkSubmitWait_OneTask(b *testing.B) {
	p := pool.NewPool(context.Background(), pool.Config{Workers: 4, Queue: 64})
	ctx := context.Background()
	var wg sync.WaitGroup
	task := func(context.Context) error {
		wg.Done()
		return nil
	}
	b.ReportAllocs()
	for b.Loop() {
		wg.Add(1)
		if err := p.Submit(ctx, task); err != nil {
			b.Fatalf("Submit: %v", err)
		}
		wg.Wait()
	}
	b.StopTimer()
	if err := p.Drain(ctx); err != nil {
		b.Fatalf("Drain: %v", err)
	}
}
