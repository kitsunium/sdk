// Package cache — white-box tests for the expiry heap and the node's layout:
// a slot out of step with its cell, or a heap order broken by a sift, shows up
// from outside only as an entry reaped late or never, so both are checked here
// where they are maintained.
package cache

import (
	"math/rand/v2"
	"reflect"
	"testing"
)

// assertHeapIntact checks that every cell's entry names the cell it sits in
// and that no child's deadline precedes its parent's.
func assertHeapIntact[K comparable, V any](t *testing.T, h *deadlineHeap[K, V]) {
	t.Helper()
	for index, cell := range h.slots {
		if got := int(cell.ent.slot) - 1; got != index {
			t.Fatalf("cell %d holds an entry whose slot names cell %d", index, got)
		}
		if cell.ent.deadline != cell.at {
			t.Fatalf("cell %d: deadline %d inline, %d on the entry", index, cell.at, cell.ent.deadline)
		}
		if index > 0 {
			if parent := (index - 1) / heapArity; h.slots[parent].at > cell.at {
				t.Fatalf("cell %d (%d) precedes its parent %d (%d)", index, cell.at, parent, h.slots[parent].at)
			}
		}
	}
}

// Test_deadlineHeap pins the 4-ary heap against a model: deadlines pushed,
// re-stamped and removed from anywhere at random, then drained from the top,
// must come out earliest first, every entry's slot in step with its cell, and
// a removed entry must carry no slot.
func Test_deadlineHeap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		entries int
	}
	tests := []tc{
		{"one entry", 1},
		{"a root and its four children", 5},
		{"three levels", 30},
		{"a thousand entries", 1000},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rng := rand.New(rand.NewPCG(uint64(c.entries), 1038))
		var h deadlineHeap[int, int]
		entries := make([]*entry[int, int], c.entries)
		for i := range entries {
			entries[i] = &entry[int, int]{key: i}
			entries[i].deadline = rng.Int64N(1000) - 500
			h.push(entries[i], entries[i].deadline)
		}
		assertHeapIntact(t, &h)
		//: re-stamp a third, forward or back.
		for _, ent := range entries[:len(entries)/3] {
			ent.deadline = rng.Int64N(1000) - 500
			h.fix(ent, ent.deadline)
			assertHeapIntact(t, &h)
		}
		//: remove every fourth from wherever it sits.
		for i := 0; i < len(entries); i += 4 {
			h.remove(entries[i])
			if entries[i].slot != 0 {
				t.Fatalf("entry %d keeps slot %d after its removal", i, entries[i].slot)
			}
			assertHeapIntact(t, &h)
		}
		want := len(entries) - (len(entries)+3)/4
		if h.size() != want {
			t.Fatalf("size = %d, want %d", h.size(), want)
		}
		//: drain from the top: earliest first.
		last := int64(-1 << 63)
		for h.size() > 0 {
			top, at := h.top()
			if at < last {
				t.Fatalf("top %d after %d: out of order", at, last)
			}
			last = at
			h.remove(top)
			assertHeapIntact(t, &h)
		}
		if top, _ := h.top(); top != nil {
			t.Errorf("an empty heap has top %v", top)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_entry_size pins the node's budget (#1038): a string key and a
// word-sized value fit one cache line — the 64-byte size class on a 64-bit
// platform, less on a 32-bit one. A field that pushes it past 64 bytes moves
// every insertion to the next size class and every node onto two lines.
func Test_entry_size(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		size uintptr
	}
	tests := []tc{
		{"a string key and an int value", reflect.TypeFor[entry[string, int]]().Size()},
		{"a string key and a pointer value", reflect.TypeFor[entry[string, *int]]().Size()},
		{"an int key and an int value", reflect.TypeFor[entry[int, int]]().Size()},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if c.size > uintptr(cacheLine) {
			t.Errorf("entry is %d bytes, over the %d of one cache line", c.size, cacheLine)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
