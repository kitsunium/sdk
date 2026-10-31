// Package loop — a ring's slot table grows by bounded steps up to its
// table size, on every platform.
package loop

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestSlotCap_steps pins the growth rule: doubling from 64 up to slotStep,
// then slotStep at a time, never past the table.
func TestSlotCap_steps(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// cur and table are the capacity and the table size.
		cur, table int
		// want is the capacity it grows to.
		want int
	}
	tests := []tc{
		{name: "empty starts at 64", cur: 0, table: 65536, want: 64},
		{name: "small doubles", cur: 1024, table: 65536, want: 2048},
		{name: "at the step doubles once more", cur: slotStep, table: 65536, want: 2 * slotStep},
		{name: "past the step grows by it", cur: 3 * slotStep, table: 65536, want: 4 * slotStep},
		{name: "bounded by the table", cur: 64000, table: 65536, want: 65536},
		{name: "a tiny table", cur: 0, table: 2, want: 2},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := slotCap(c.cur, c.table); got != c.want {
				t.Errorf("slotCap(%d, %d) = %d, want %d", c.cur, c.table, got, c.want)
			}
		})
	}
}

// TestRing_tableGrowsByStep opens connections one by one on a ring and
// checks that its table, and the wheel beside it, never hold more than
// slotStep unused slots once past slotStep.
func TestRing_tableGrowsByStep(t *testing.T) {
	t.Parallel()
	_, r, _ := newTestLoop(&scriptH{}, corenet.LoopValue{}, 0)
	r.lp.sz.tableSize = 5*slotStep + 7
	for i := range r.lp.sz.tableSize {
		if _, ok := r.take(int32(i)); !ok {
			t.Fatalf("take %d refused under a table of %d", i, r.lp.sz.tableSize)
		}
		n := len(r.conns)
		if c := cap(r.conns); c > max(2*n, 64) && c-n > slotStep {
			t.Fatalf("%d slots in a table of capacity %d", n, c)
		}
		if cap(r.conns) > r.lp.sz.tableSize || len(r.wheel.at) != n {
			t.Fatalf("%d slots: capacity %d, wheel %d, table %d", n, cap(r.conns), len(r.wheel.at), r.lp.sz.tableSize)
		}
	}
	if _, ok := r.take(0); ok {
		t.Fatal("a take past the table succeeded")
	}
}
