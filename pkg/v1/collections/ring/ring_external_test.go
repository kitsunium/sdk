package ring_test

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/collections/ring"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// TestNewRefusesACapacityThatHoldsNothing asserts CapZero for every capacity
// that is not positive, matched by errors.Is and by code through pkg/v1/errs.
func TestNewRefusesACapacityThatHoldsNothing(t *testing.T) {
	t.Parallel()
	code, ok := errs.CodeOf(ring.CapZero)
	if !ok {
		t.Fatal("CapZero carries no code")
	}
	for _, capacity := range []int{0, -1} {
		q, err := ring.New[int](capacity)
		if q != nil || !errors.Is(err, ring.CapZero) || !errs.HasCode(err, code) {
			t.Errorf("New(%d) = %v, %v; want nil, CapZero", capacity, q, err)
		}
	}
}

// TestTheRingRefusesInsteadOfBlocking asserts the bounded FIFO through the
// public alias: writes until Full, Len and Capacity as it fills, reads in
// order until Empty — round after round, so both cursors wrap. Three slots
// back a capacity of two, a count that is not a power of two: the one where a
// wrapped ring's Len used to be wrong.
func TestTheRingRefusesInsteadOfBlocking(t *testing.T) {
	t.Parallel()
	const capacity int = 2
	q, err := ring.New[int](capacity)
	if err != nil {
		t.Fatalf("New(%d) = %v", capacity, err)
	}
	for round := range 4 {
		for i := range capacity {
			if err := q.TryWrite(round*10 + i); err != nil {
				t.Fatalf("round %d: TryWrite %d = %v", round, i, err)
			}
		}
		if err := q.TryWrite(99); !errors.Is(err, ring.Full) {
			t.Errorf("round %d: TryWrite on a full ring = %v, want Full", round, err)
		}
		if q.Len() != capacity || q.Capacity() != capacity {
			t.Errorf("round %d: Len %d, Capacity %d; want %d and %d", round, q.Len(), q.Capacity(), capacity, capacity)
		}
		for i := range capacity {
			if v, err := q.TryRead(); err != nil || v != round*10+i {
				t.Errorf("round %d: TryRead = %d, %v; want %d, nil", round, v, err, round*10+i)
			}
		}
		if _, err := q.TryRead(); !errors.Is(err, ring.Empty) || q.Len() != 0 {
			t.Errorf("round %d: TryRead on an empty ring = %v with Len %d, want Empty and 0", round, err, q.Len())
		}
	}
}

// TestOneProducerOneConsumer runs the one topology the ring supports — one
// goroutine writing, another reading — and asserts every item arrives once,
// in order. The race detector is the other half of the assertion.
func TestOneProducerOneConsumer(t *testing.T) {
	t.Parallel()
	const items int = 10_000
	q, err := ring.New[int](64)
	if err != nil {
		t.Fatalf("New(64) = %v", err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < items; {
			if q.TryWrite(i) != nil {
				runtime.Gosched() // full: let the consumer run
				continue
			}
			i++
		}
	})
	misplaced := 0
	for next := 0; next < items; {
		v, err := q.TryRead()
		if err != nil {
			runtime.Gosched() // empty: let the producer run
			continue
		}
		if v != next {
			misplaced++
		}
		next++
	}
	wg.Wait()
	if misplaced != 0 {
		t.Errorf("%d of %d items arrived out of order", misplaced, items)
	}
}

// ExampleNew hands items from one side to the other: a write past the
// capacity is refused, and reading stops at the first Empty.
func ExampleNew() {
	q, err := ring.New[string](2)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, item := range []string{"first", "second", "third"} {
		if err := q.TryWrite(item); err != nil {
			fmt.Println(item, "refused, full:", errors.Is(err, ring.Full))
		}
	}
	for {
		item, err := q.TryRead()
		if err != nil {
			fmt.Println("then empty:", errors.Is(err, ring.Empty))
			return
		}
		fmt.Println(item)
	}
	// Output:
	// third refused, full: true
	// first
	// second
	// then empty: true
}
