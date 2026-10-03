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
