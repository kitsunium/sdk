package ring

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func mustQueue(tb testing.TB, capacity int) *queueRing[int] {
	tb.Helper()
	q := &queueRing[int]{slots: allocateSlots[int](capacity + 1), cap: uint64(capacity)}
	return q
}

func Test_queueRing_Capacity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cap  int
	}{
		{"capacity 4 round-trips", 4},
		{"capacity 1 round-trips", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := mustQueue(t, tc.cap)
			if got := q.Capacity(); got != tc.cap {
				t.Errorf("Capacity = %d, want %d", got, tc.cap)
			}
		})
	}
}

func Test_queueRing_Len(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		writes  int
		wantLen int
	}{
		{"empty ring has len 0", 0, 0},
		{"two writes give len 2", 2, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := mustQueue(t, 4)
			for i := range tc.writes {
				if err := q.TryWrite(i); err != nil {
					t.Fatalf("TryWrite err = %v", err)
				}
			}
			if got := q.Len(); got != tc.wantLen {
				t.Errorf("Len = %d, want %d", got, tc.wantLen)
			}
		})
	}
}

// lenAfter builds a ring of the given capacity, walks both cursors to start
// (a write and a read each step), writes held items, and returns Len.
func lenAfter(tb testing.TB, capacity, start, held int) int {
	tb.Helper()
	q := mustQueue(tb, capacity)
	for i := range start {
		if err := q.TryWrite(i); err != nil {
			tb.Fatalf("TryWrite while walking the cursors: %v", err)
		}
		if _, err := q.TryRead(); err != nil {
			tb.Fatalf("TryRead while walking the cursors: %v", err)
		}
	}
	for i := range held {
		if err := q.TryWrite(i); err != nil {
			tb.Fatalf("TryWrite %d of %d: %v", i+1, held, err)
		}
	}
	return q.Len()
}

// Test_queueRing_LenAfterWrap pins Len once the write cursor has wrapped past
// the read cursor. A bare tail-head wraps modulo 2^64, which cap+1 divides
// only when it is a power of two: the old arithmetic reported 2 for a ring of
// capacity 2 holding one item, and an EMPTY ring for the async logger's
// default capacity holding 1009 — a state in which its Flush, waiting for
// Len() == 0, could return with those records still queued.
func Test_queueRing_LenAfterWrap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		capacity, start, at int
	}{
		{"capacity 2 holding one item after the wrap", 2, 2, 1},
		{"capacity 1024 holding 1009 after the wrap, read as empty before", 1024, 1024, 1009},
		{"capacity 1024 holding one item after the wrap", 1024, 1024, 1},
		{"capacity 3, four slots, a power of two", 3, 3, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := lenAfter(t, tc.capacity, tc.start, tc.at); got != tc.at {
				t.Errorf("Len = %d, want %d", got, tc.at)
			}
		})
	}
}

// Test_queueRing_LenAtEveryCursor checks Len against the number of items
// written at every cursor position and every fill level of small rings, slot
// counts that are and are not powers of two.
func Test_queueRing_LenAtEveryCursor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		capacity int
	}{
		{"capacity 1", 1},
		{"capacity 2", 2},
		{"capacity 3", 3},
		{"capacity 6", 6},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for start := range tc.capacity + 1 {
				for held := range tc.capacity + 1 {
					if got := lenAfter(t, tc.capacity, start, held); got != held {
						t.Errorf("start %d, %d held: Len = %d", start, held, got)
					}
				}
			}
		})
	}
}

func Test_queueRing_TryWrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"writes succeed until the ring is saturated"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := mustQueue(t, 2)
			if err := q.TryWrite(1); err != nil {
				t.Errorf("first TryWrite err = %v", err)
			}
			if err := q.TryWrite(2); err != nil {
				t.Errorf("second TryWrite err = %v", err)
			}
			if err := q.TryWrite(3); !errs.HasCode(err, CodeRingFull) {
				t.Errorf("third TryWrite err = %v, want Full", err)
			}
		})
	}
}

func Test_queueRing_TryRead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"reads succeed until the ring is empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := mustQueue(t, 2)
			if err := q.TryWrite(7); err != nil {
				t.Fatalf("TryWrite err = %v", err)
			}
			got, err := q.TryRead()
			if err != nil {
				t.Errorf("TryRead err = %v", err)
			}
			if got != 7 {
				t.Errorf("TryRead = %d, want 7", got)
			}
			if _, rerr := q.TryRead(); !errs.HasCode(rerr, CodeRingEmpty) {
				t.Errorf("TryRead on empty ring err = %v, want Empty", rerr)
			}
		})
	}
}

func Test_allocateSlots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		n    int
	}{
		{"one slot", 1},
		{"sixteen slots", 16},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := allocateSlots[int](tc.n)
			if len(got) != tc.n {
				t.Errorf("allocateSlots len = %d, want %d", len(got), tc.n)
			}
		})
	}
}
