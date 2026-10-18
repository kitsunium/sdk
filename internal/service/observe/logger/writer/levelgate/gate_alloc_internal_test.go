//go:build !race

// Package levelgate — the allocation contract a severity floor is bought for.
//
// A gate runs on every record a writer is handed, including every one it
// discards. Its cost is therefore paid by exactly the callers who configured it
// so that work would not happen, which makes a heap allocation on the drop path
// not a datum but a defect: a consumer who sets MinLevel=Error to make debug
// logging free would instead be paying the garbage collector for every debug
// line their service does not emit.
//
// Both directions are pinned, because "the drop is free" is only half a
// contract — a gate that allocated on the way THROUGH would tax every record it
// was installed to let past.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so any malloc count under `-race`
// measures the detector. That makes this file invisible to the race suite,
// which is why //internal/service/observe/logger/writer/levelgate:levelgate_test carries an
// entry in tools/alloc-lane-targets.txt — the race-off alloc lane is its ONLY
// gate (SDK-wide rule 12).
package levelgate

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
)

// allocRuns is how many times each claim is exercised. It is large enough that
// an amortised allocation — one that happens on a growth step rather than on
// every call — has happened several times by the end.
const allocRuns int = 500

// allocPayload is an ordinary encoded line. The drop path returns len(p) and
// never reads the bytes, so the size is not the subject — but a token payload
// would let a future defect that copies the slice hide behind a small one.
var allocPayload = []byte(
	`2026-09-10T20:15:11.482Z DEBUG msg="cache lookup" key=session:41 hit=false` + "\n",
)

// TestGateAllocatesNothingInEitherDirection is the guard behind this package's
// reason to exist.
//
// The drop arm is the headline: 7.04 ns and zero allocations, against 6.32 ns
// for the same record reaching a sink with no gate installed at all
// (BENCH.md §1). The pass arm is the other half — the gate must not tax the
// records it lets through either, and at 15.06 ns it does not, all of the
// difference being a second interface call rather than anything on the heap.
//
// MUTATION: adding drop accounting to gateSink — a `dropped []level.Level`
// field and `s.dropped = append(s.dropped, r.Level)` inside the
// `r.Level < s.min` branch, which is the first thing anyone reaches for when
// asked how many records the floor is discarding — fails at
// `500 dropped records performed 6 allocations, want 0`. Moving that same
// append ABOVE the branch, so it runs for every record, additionally fails the
// second assertion at
// `500 records passed by the gate performed 2 allocations, want 0`. Both counts
// reproduced identically across repeated runs.
//
// Six, not five hundred: the slice doubles, so the allocation happens on the
// growth steps and not on the calls between them. That is the whole reason this
// test counts a TOTAL rather than calling testing.AllocsPerRun, which reported
// 0.0 for this very mutation and let the guard pass — see mallocsOver, in
// allocwindow_test.go, which counts that total on the window's own stack.
func TestGateAllocatesNothingInEitherDirection(t *testing.T) {
	ctx := context.Background()
	//: a real floor, so New installs the gate rather than handing inner back.
	gate := New(noopSink{}, level.Error)

	below := corelogger.RecordEvent{Level: level.Debug}
	dropped := mallocsOver(allocRuns, func() {
		if n, err := gate.Write(ctx, below, allocPayload); err != nil || n != len(allocPayload) {
			t.Fatalf("dropping a record reported (%d, %v), want (%d, nil)", n, err, len(allocPayload))
		}
	})
	reportWindow(t, "dropped records", dropped)
	if dropped.own != 0 {
		t.Errorf("%d dropped records performed %d allocations, want 0", allocRuns, dropped.own)
	}

	above := corelogger.RecordEvent{Level: level.Error}
	passed := mallocsOver(allocRuns, func() {
		if n, err := gate.Write(ctx, above, allocPayload); err != nil || n != len(allocPayload) {
			t.Fatalf("passing a record reported (%d, %v), want (%d, nil)", n, err, len(allocPayload))
		}
	})
	reportWindow(t, "passed records", passed)
	if passed.own != 0 {
		t.Errorf("%d records passed by the gate performed %d allocations, want 0", allocRuns, passed.own)
	}
}
