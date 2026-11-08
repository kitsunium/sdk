package sema_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/concur/sema"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// waitQueued polls until try reports false, i.e. the earlier Acquire is
// parked, so the next step happens behind it.
func waitQueued(t *testing.T, s *sema.Weighted) {
	t.Helper()
	for range 1000 {
		//: a waiter ahead makes TryAcquire(0) refuse.
		if !s.TryAcquire(0) {
			return
		}
		s.Release(0)
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the Acquire never queued")
}

func TestAcquireTooHeavyIsRefusedAtOnce(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		capacity int64
		n        int64
	}{
		{"one over the capacity", 4, 5},
		{"a clamped capacity of one", 0, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := sema.NewWeighted(tc.capacity)
			//: no ctx deadline: a parked call would hang the test, not fail it.
			if err := s.Acquire(context.Background(), tc.n); !errs.HasCode(err, sema.CodeWeightTooLarge) {
				t.Errorf("Acquire(%d) on capacity %d = %v, want WeightTooLarge", tc.n, tc.capacity, err)
			}
			if s.TryAcquire(tc.n) {
				t.Errorf("TryAcquire(%d) on capacity %d = true", tc.n, tc.capacity)
			}
		})
	}
}

func TestAcquireAndRelease(t *testing.T) {
	t.Parallel()
	s := sema.NewWeighted(10)
	if err := s.Acquire(t.Context(), 7); err != nil {
		t.Fatalf("Acquire(7): %v", err)
	}
	if s.TryAcquire(4) {
		t.Error("TryAcquire(4) with 3 free = true")
	}
	if !s.TryAcquire(3) {
		t.Error("TryAcquire(3) with 3 free = false")
	}
	s.Release(10)
	if err := s.Acquire(t.Context(), 10); err != nil {
		t.Errorf("Acquire of the whole capacity after Release: %v", err)
	}
}

func TestAcquireOnADoneContextHoldsNothing(t *testing.T) {
	t.Parallel()
	s := sema.NewWeighted(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Acquire(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire on a done ctx = %v, want context.Canceled", err)
	}
	if !s.TryAcquire(1) {
		t.Error("the refused Acquire kept its unit")
	}
}

// TestCancelledWaiterLeavesTheQueue is #812's: a waiter whose context ends
// leaves the queue holding nothing, and a later waiter is admitted.
func TestCancelledWaiterLeavesTheQueue(t *testing.T) {
	t.Parallel()
	s := sema.NewWeighted(4)
	if err := s.Acquire(t.Context(), 4); err != nil {
		t.Fatalf("Acquire(4): %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- s.Acquire(ctx, 3) }()
	waitQueued(t, s)
	later := make(chan error, 1)
	go func() { later <- s.Acquire(context.Background(), 1) }()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled waiter = %v, want context.Canceled", err)
	}
	s.Release(1)
	select {
	case err := <-later:
		if err != nil {
			t.Errorf("the later waiter = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the later waiter was not admitted after the cancelled one left")
	}
	//: the books balance: 3 held by the first Acquire, 1 by the later one.
	s.Release(4)
	if !s.TryAcquire(4) {
		t.Error("units leaked through the cancellation")
	}
}

// TestFIFOLargeBeforeSmall is #812's: a waiting Acquire(8) is admitted before
// a later Acquire(1) once 8 units free — the small one does not overtake.
func TestFIFOLargeBeforeSmall(t *testing.T) {
	t.Parallel()
	s := sema.NewWeighted(8)
	if err := s.Acquire(t.Context(), 8); err != nil {
		t.Fatalf("Acquire(8): %v", err)
	}
	var order []int
	var mu sync.Mutex
	record := func(who int) {
		mu.Lock()
		order = append(order, who)
		mu.Unlock()
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := s.Acquire(context.Background(), 8); err == nil {
			record(8)
			s.Release(8)
		}
	})
	waitQueued(t, s)
	wg.Go(func() {
		if err := s.Acquire(context.Background(), 1); err == nil {
			record(1)
			s.Release(1)
		}
	})
	//: one unit free: enough for the small request, not for the large one ahead.
	s.Release(1)
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	early := len(order)
	mu.Unlock()
	if early != 0 {
		t.Fatalf("a waiter was admitted ahead of its turn: %v", order)
	}
	s.Release(7)
	wg.Wait()
	if len(order) != 2 || order[0] != 8 {
		t.Errorf("admission order = %v, want [8 1]", order)
	}
}

// TestNeverOverAdmits: many goroutines acquire and release mixed weights; the
// units in use never exceed the capacity, and every call is served.
func TestNeverOverAdmits(t *testing.T) {
	t.Parallel()
	const capacity = 16
	s := sema.NewWeighted(capacity)
	var inUse, peak atomic.Int64
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			for i := range 200 {
				n := int64((g+i)%capacity + 1)
				if err := s.Acquire(context.Background(), n); err != nil {
					t.Errorf("Acquire(%d): %v", n, err)
					return
				}
				now := inUse.Add(n)
				for p := peak.Load(); now > p && !peak.CompareAndSwap(p, now); p = peak.Load() {
				}
				inUse.Add(-n)
				s.Release(n)
			}
		})
	}
	wg.Wait()
	if peak.Load() > capacity {
		t.Errorf("peak units in use = %d, capacity %d", peak.Load(), capacity)
	}
}

func TestMisusePanics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(s *sema.Weighted)
	}{
		{"Release of more than is held", func(s *sema.Weighted) { s.Release(1) }},
		{"a negative Release", func(s *sema.Weighted) { s.Release(-1) }},
		{"a negative Acquire", func(s *sema.Weighted) { _ = s.Acquire(context.Background(), -1) }},
		{"a negative TryAcquire", func(s *sema.Weighted) { _ = s.TryAcquire(-1) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", tc.name)
				}
			}()
			tc.call(sema.NewWeighted(4))
		})
	}
}
