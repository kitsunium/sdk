package ring_test

import (
	"runtime"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/ring"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestNewMulti(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		capacity     int
		wantCode     errs.Code
		wantCapacity int
	}{
		{"a power of two is kept", 8, 0, 8},
		{"one is a power of two", 1, 0, 1},
		{"a capacity rounds up to the next power of two", 1000, 0, 1024},
		{"zero capacity is rejected", 0, ring.CodeRingCapZero, 0},
		{"negative capacity is rejected", -3, ring.CodeRingCapZero, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q, err := ring.NewMulti[int](tc.capacity)
			//: a refused capacity names its sentinel and builds nothing.
			if tc.wantCode != 0 {
				if !errs.HasCode(err, tc.wantCode) || q != nil {
					t.Errorf("NewMulti(%d) = %v, %v; want nil, code %v", tc.capacity, q, err, tc.wantCode)
				}
				return
			}
			if err != nil || q.Capacity() != tc.wantCapacity {
				t.Fatalf("NewMulti(%d): capacity %d, err %v; want %d, nil", tc.capacity, q.Capacity(), err, tc.wantCapacity)
			}
		})
	}
}

func TestMultiQueue_FullEmptyAndOrder(t *testing.T) {
	t.Parallel()
	q, err := ring.NewMulti[int](4)
	if err != nil {
		t.Fatalf("NewMulti: %v", err)
	}
	if _, rerr := q.TryRead(); !errs.HasCode(rerr, ring.CodeRingEmpty) {
		t.Errorf("TryRead on an empty ring = %v, want Empty", rerr)
	}
	//: three laps round the four cells: order held, full refused, empty answered.
	next := 0
	for lap := range 3 {
		for i := range 4 {
			if werr := q.TryWrite(lap*4 + i); werr != nil {
				t.Fatalf("lap %d TryWrite %d: %v", lap, i, werr)
			}
		}
		if q.Len() != 4 {
			t.Errorf("lap %d: Len = %d, want 4", lap, q.Len())
		}
		if werr := q.TryWrite(-1); !errs.HasCode(werr, ring.CodeRingFull) {
			t.Fatalf("lap %d: TryWrite on a full ring = %v, want Full", lap, werr)
		}
		for got := range ring.Drain[int](q) {
			if got != next {
				t.Fatalf("lap %d: read %d, want %d", lap, got, next)
			}
			next++
		}
		if q.Len() != 0 {
			t.Errorf("lap %d: Len after the drain = %d, want 0", lap, q.Len())
		}
	}
}

// TestMultiQueue_ProducersOneConsumer is #805's acceptance: eight writers and
// one reader move a million records under the race detector; every record is
// read exactly once, and each writer's records come out in the order it wrote
// them.
func TestMultiQueue_ProducersOneConsumer(t *testing.T) {
	t.Parallel()
	const producers = 8
	total := 1_000_000
	//: the short lane keeps the shape and drops two orders of magnitude.
	if testing.Short() {
		total = 10_000
	}
	perProducer := total / producers
	q, err := ring.NewMulti[uint64](1024)
	if err != nil {
		t.Fatalf("NewMulti: %v", err)
	}
	var wg sync.WaitGroup
	for p := range producers {
		wg.Go(func() {
			for i := range perProducer {
				record := uint64(p)<<32 | uint64(i)
				//: a full ring is retried: the test counts records, not drops.
				for q.TryWrite(record) != nil {
					runtime.Gosched()
				}
			}
		})
	}
	nextOf := make([]int, producers)
	for read := 0; read < perProducer*producers; {
		record, rerr := q.TryRead()
		//: nothing published yet: let a writer run.
		if rerr != nil {
			runtime.Gosched()
			continue
		}
		p, i := int(record>>32), int(record&0xffffffff)
		//: a writer's records arrive in its own order, each exactly once.
		if i != nextOf[p] {
			t.Fatalf("writer %d: read record %d, want %d", p, i, nextOf[p])
		}
		nextOf[p]++
		read++
	}
	wg.Wait()
	if _, rerr := q.TryRead(); !errs.HasCode(rerr, ring.CodeRingEmpty) {
		t.Errorf("TryRead after every record = %v, want Empty", rerr)
	}
}
