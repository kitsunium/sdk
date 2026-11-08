package sema_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/sema"
)

// BenchmarkAcquire_Uncontended is one Acquire(1) and its Release on a free
// semaphore: the cost an admission control adds to a call that is admitted
// at once. #812's acceptance: 0 allocs/op and at most twice the chan
// struct{} baseline below.
func BenchmarkAcquire_Uncontended(b *testing.B) {
	s := sema.NewWeighted(64)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if err := s.Acquire(ctx, 1); err != nil {
			b.Fatalf("Acquire: %v", err)
		}
		s.Release(1)
	}
}

// BenchmarkChanBaseline_Uncontended is the unit semaphore the resilience
// bulkhead is built on — a buffered chan struct{}, a send to take a slot and
// a receive to give it back — for Acquire_Uncontended to be read against.
func BenchmarkChanBaseline_Uncontended(b *testing.B) {
	slots := make(chan struct{}, 64)
	b.ReportAllocs()
	for b.Loop() {
		slots <- struct{}{}
		<-slots
	}
}

// BenchmarkAcquire_Parallel is Acquire(1) and Release from every P at once on
// a semaphore of 64 units: contention on the semaphore's lock, rarely a wait.
func BenchmarkAcquire_Parallel(b *testing.B) {
	s := sema.NewWeighted(64)
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := s.Acquire(ctx, 1); err != nil {
				b.Errorf("Acquire: %v", err)
				return
			}
			s.Release(1)
		}
	})
}

// BenchmarkChanBaseline_Parallel is the chan struct{} semaphore under the
// same load, the baseline Acquire_Parallel is read against.
func BenchmarkChanBaseline_Parallel(b *testing.B) {
	slots := make(chan struct{}, 64)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			slots <- struct{}{}
			<-slots
		}
	})
}

// BenchmarkAcquire_Contended_FIFO is every P acquiring 3 units of 4, so
// nearly every Acquire queues behind another and is granted by a Release:
// the waiter's path, its allocation included.
func BenchmarkAcquire_Contended_FIFO(b *testing.B) {
	s := sema.NewWeighted(4)
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := s.Acquire(ctx, 3); err != nil {
				b.Errorf("Acquire: %v", err)
				return
			}
			s.Release(3)
		}
	})
}
