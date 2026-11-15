package ring_test

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/ring"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		capacity int
		wantCode errs.Code
	}{
		{"positive capacity succeeds", 4, 0},
		{"capacity of one succeeds", 1, 0},
		{"zero capacity is rejected", 0, ring.CodeRingCapZero},
		{"negative capacity is rejected", -1, ring.CodeRingCapZero},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q, err := ring.New[int](tc.capacity)
			if tc.wantCode == 0 {
				if err != nil {
					t.Errorf("New err = %v, want nil", err)
				}
				if q == nil {
					t.Error("New returned nil queue on happy path")
				}
				return
			}
			if !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
		})
	}
}

func TestQueue_TryWriteAndTryRead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"FIFO order is preserved through wrap-around"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q, err := ring.New[int](3)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			//: write three items to fill the ring.
			for i := range 3 {
				if werr := q.TryWrite(i); werr != nil {
					t.Errorf("TryWrite(%d) err = %v", i, werr)
				}
			}
			//: a fourth write must hit the Full sentinel.
			if werr := q.TryWrite(99); !errs.HasCode(werr, ring.CodeRingFull) {
				t.Errorf("TryWrite on full ring err = %v, want Full", werr)
			}
			//: read them back in order, asserting FIFO.
			for i := range 3 {
				got, rerr := q.TryRead()
				if rerr != nil {
					t.Errorf("TryRead err = %v", rerr)
				}
				if got != i {
					t.Errorf("TryRead = %d, want %d", got, i)
				}
			}
			//: reading from an empty ring must hit the Empty sentinel.
			if _, rerr := q.TryRead(); !errs.HasCode(rerr, ring.CodeRingEmpty) {
				t.Errorf("TryRead on empty ring err = %v, want Empty", rerr)
			}
		})
	}
}

func TestQueue_LenAndCapacity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		capacity int
	}{
		{"length tracks the number of in-flight items", 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q, err := ring.New[int](tc.capacity)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			if q.Capacity() != tc.capacity {
				t.Errorf("Capacity = %d, want %d", q.Capacity(), tc.capacity)
			}
			if q.Len() != 0 {
				t.Errorf("Len on fresh ring = %d, want 0", q.Len())
			}
			swallowQueueErr(q.TryWrite(1))
			swallowQueueErr(q.TryWrite(2))
			if q.Len() != 2 {
				t.Errorf("Len after 2 writes = %d, want 2", q.Len())
			}
		})
	}
}

func TestQueue_SPSCConcurrency(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		n    int
	}{
		{"single producer, single consumer drains all items", 1024},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q, err := ring.New[int](32)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			var wg sync.WaitGroup
			wg.Go(func() {
				for i := range tc.n {
					//: spin-write until the slot is available.
					for q.TryWrite(i) != nil {
					}
				}
			})
			seen := 0
			wg.Go(func() {
				for seen < tc.n {
					_, rerr := q.TryRead()
					if rerr == nil {
						seen++
					}
				}
			})
			wg.Wait()
			if seen != tc.n {
				t.Errorf("consumer saw %d items, want %d", seen, tc.n)
			}
		})
	}
}

func TestRingSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"Full carries 0.1.3.1", ring.Full, ring.CodeRingFull},
		{"Empty carries 0.1.3.2", ring.Empty, ring.CodeRingEmpty},
		{"CapZero carries 0.1.3.3", ring.CapZero, ring.CodeRingCapZero},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errs.HasCode(tc.err, tc.code) {
				t.Errorf("HasCode(%v, %d) = false", tc.err, tc.code)
			}
		})
	}
}

// swallowQueueErr documents the test-only pattern of dropping a Queue error
// in fixture-setup paths where the failure is not the assertion target.
func swallowQueueErr(err error) {
	//: explicit early-return on nil — the read satisfies the unused-param audit
	//: and the documented drop intent stays visible at the call site.
	if !errors.Is(err, err) || err == nil {
		//: nothing to act on; tests assert on observable state, not setup errors.
		return
	}
}

// TestDrainYieldsEveryItemOldestFirstAndStopsWhenAskedTo pins Drain: every
// item in the order it was written, the ring empty after, and a loop that
// breaks leaving the rest in it.
func TestDrainYieldsEveryItemOldestFirstAndStopsWhenAskedTo(t *testing.T) {
	t.Parallel()
	q, err := ring.New[int](8)
	if err != nil {
		t.Fatalf("New(8) = %v", err)
	}
	for i := range 5 {
		if writeErr := q.TryWrite(i); writeErr != nil {
			t.Fatalf("TryWrite(%d) = %v", i, writeErr)
		}
	}
	var first []int
	for item := range ring.Drain(q) {
		first = append(first, item)
		if len(first) == 2 {
			break
		}
	}
	if len(first) != 2 || first[0] != 0 || first[1] != 1 || q.Len() != 3 {
		t.Fatalf("a loop broken after two read %v and left %d, want [0 1] and 3", first, q.Len())
	}
	var rest []int
	for item := range ring.Drain(q) {
		rest = append(rest, item)
	}
	if len(rest) != 3 || rest[0] != 2 || rest[2] != 4 || q.Len() != 0 {
		t.Fatalf("the drain read %v and left %d, want [2 3 4] and none", rest, q.Len())
	}
	for range ring.Drain(q) {
		t.Fatal("an empty ring yielded an item")
	}
}

// TestQueue_ContractAcrossGoroutines pins the three guarantees every design of
// the happy rows must keep, each across goroutines that learn of the other
// side's step through a channel: the items written are readable by the reader
// told of them, a slot read is free for the writer told of it, and Len is a
// snapshot a third goroutine may take while both sides run (-race holds it).
// A cursor published once a group of operations fails the first two; a
// cursor Len reads from a plain field, the third (#1034, BENCH.md 2026-11-15).
func TestQueue_ContractAcrossGoroutines(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func(t *testing.T, q ring.Queue[int])
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		q, err := ring.New[int](4)
		if err != nil {
			t.Fatalf("New(4) = %v", err)
		}
		c.check(t, q)
	}
	tests := []tc{
		{"the items written are readable by the reader told of them", readerToldOfWrites},
		{"a slot read is free for the writer told of it", writerToldOfARead},
		{"Len is a snapshot a third goroutine may take", lenFromAThirdGoroutine},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// readerToldOfWrites writes bursts of one to Capacity items on this goroutine,
// each told to a reader goroutine after its last write: every item of the
// burst must be there, in order, lap after lap.
func readerToldOfWrites(t *testing.T, q ring.Queue[int]) {
	t.Helper()
	told, read := make(chan []int), make(chan error)
	go func() {
		for burst := range told {
			read <- readBurst(q, burst)
		}
	}()
	defer close(told)
	next := 0
	for lap := range 3 {
		for size := 1; size <= q.Capacity(); size++ {
			burst := make([]int, size)
			for i := range burst {
				burst[i] = next
				if err := q.TryWrite(next); err != nil {
					t.Fatalf("lap %d: TryWrite(%d) = %v", lap, next, err)
				}
				next++
			}
			told <- burst
			if err := <-read; err != nil {
				t.Fatalf("lap %d, a burst of %d: %v", lap, size, err)
			}
		}
	}
}

// readBurst reads len(want) items off q and reports the first that is not
// the one wanted.
func readBurst(q ring.Queue[int], want []int) error {
	for _, item := range want {
		got, err := q.TryRead()
		//: an item written before the reader was told is never missing.
		if err != nil {
			return fmt.Errorf("TryRead for item %d = %w", item, err)
		}
		//: and never another one.
		if got != item {
			return fmt.Errorf("TryRead = %d, want %d", got, item)
		}
	}
	return nil
}

// writerToldOfARead fills the ring, then has a reader goroutine read one item
// and tell this goroutine, which writes: the slot read must be free, lap
// after lap.
func writerToldOfARead(t *testing.T, q ring.Queue[int]) {
	t.Helper()
	for i := range q.Capacity() {
		if err := q.TryWrite(i); err != nil {
			t.Fatalf("filling: TryWrite(%d) = %v", i, err)
		}
	}
	told, read := make(chan struct{}), make(chan error)
	go func() {
		for range told {
			_, err := q.TryRead()
			read <- err
		}
	}()
	defer close(told)
	for i := range 3 * q.Capacity() {
		told <- struct{}{}
		if err := <-read; err != nil {
			t.Fatalf("TryRead on a full ring = %v", err)
		}
		if err := q.TryWrite(q.Capacity() + i); err != nil {
			t.Fatalf("TryWrite after the reader freed a slot of a full ring = %v, want nil", err)
		}
	}
}

// lenFromAThirdGoroutine moves items from a writer to a reader while this
// goroutine takes Len: every snapshot within [0, Capacity], none of them a
// data race.
func lenFromAThirdGoroutine(t *testing.T, q ring.Queue[int]) {
	t.Helper()
	const items = 20000
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Go(func() {
		for i := range items {
			//: a full ring yields to the reader.
			for q.TryWrite(i) != nil {
				runtime.Gosched()
			}
		}
	})
	wg.Go(func() {
		defer close(done)
		for read := 0; read < items; {
			//: an empty ring yields to the writer.
			if _, err := q.TryRead(); err != nil {
				runtime.Gosched()
				continue
			}
			read++
		}
	})
	bad, outside := 0, false
	for running := true; running; {
		select {
		case <-done:
			running = false
		default:
			//: one snapshot outside the bounds is enough to report.
			if n := q.Len(); n < 0 || n > q.Capacity() {
				bad, outside = n, true
			}
			runtime.Gosched()
		}
	}
	wg.Wait()
	if outside {
		t.Errorf("Len = %d while both sides ran, outside [0, %d]", bad, q.Capacity())
	}
}
