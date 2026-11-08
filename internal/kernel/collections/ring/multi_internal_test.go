package ring

import (
	"testing"
	"unsafe"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_multiRing_ClaimedNotPublished pins the window between a writer's claim
// and its store: the reader answers Empty for that cell — and so for every
// cell behind it — until the store lands, then reads both in order. Len counts
// the claimed cell already.
func Test_multiRing_ClaimedNotPublished(t *testing.T) {
	t.Parallel()
	r := newMultiRing[int](4)
	//: a writer claims position 0 and stalls before its store.
	if !r.tail.CompareAndSwap(0, 1) {
		t.Fatal("claim of position 0 refused")
	}
	//: a second writer claims and publishes position 1.
	if err := r.TryWrite(11); err != nil {
		t.Fatalf("TryWrite: %v", err)
	}
	if got := r.Len(); got != 2 {
		t.Errorf("Len with one claim in flight = %d, want 2", got)
	}
	if _, err := r.TryRead(); !errs.HasCode(err, CodeRingEmpty) {
		t.Fatalf("TryRead of a claimed, unpublished cell = %v, want Empty", err)
	}
	//: the stalled writer publishes; both cells read in position order.
	r.slots[0].item = 10
	r.slots[0].seq.Store(1)
	for _, want := range []int{10, 11} {
		got, err := r.TryRead()
		if err != nil || got != want {
			t.Errorf("TryRead = %d, %v; want %d, nil", got, err, want)
		}
	}
}

// Test_multiRing_ReadClearsTheCell pins what TryRead leaves behind: the item
// zeroed, so the ring keeps no reference to it, and the cell's turn moved one
// lap ahead.
func Test_multiRing_ReadClearsTheCell(t *testing.T) {
	t.Parallel()
	r := newMultiRing[*int](2)
	v := 7
	if err := r.TryWrite(&v); err != nil {
		t.Fatalf("TryWrite: %v", err)
	}
	if got, err := r.TryRead(); err != nil || got != &v {
		t.Fatalf("TryRead = %v, %v", got, err)
	}
	if r.slots[0].item != nil {
		t.Error("the cell still references the item it handed out")
	}
	if got := r.slots[0].seq.Load(); got != 2 {
		t.Errorf("the cell's turn = %d, want 2 (one lap ahead)", got)
	}
}

// Test_multiRing_CursorsOnTheirOwnLines: the writers' cursor and the
// reader's are at least a cache line apart, and both a line from the header.
func Test_multiRing_CursorsOnTheirOwnLines(t *testing.T) {
	t.Parallel()
	var r multiRing[int]
	tests := []struct {
		name       string
		end, start uintptr
	}{
		{"header to the writers' cursor", unsafe.Offsetof(r.mask) + unsafe.Sizeof(r.mask), unsafe.Offsetof(r.tail)},
		{"writers' cursor to the reader's", unsafe.Offsetof(r.tail) + unsafe.Sizeof(r.tail), unsafe.Offsetof(r.head)},
		{"reader's cursor to the next object", unsafe.Offsetof(r.head) + unsafe.Sizeof(r.head), unsafe.Sizeof(r)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if gap := tc.start - tc.end; gap < cacheLine {
				t.Errorf("%d bytes apart, want at least %d", gap, cacheLine)
			}
		})
	}
}
