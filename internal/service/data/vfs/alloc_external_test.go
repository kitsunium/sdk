//go:build !race

// Package vfs_test — the allocation gate of a publication on the disk engine
// (#813). Race off, because the race detector allocates on every memory
// access: the alloc lane (tools/alloc-lane-targets.txt) is its only gate.
// Hand-written rather than a design budget (ADR 0165): kit's gate counts
// 60 000 calls, and each of these pays two fsyncs.
package vfs_test

import (
	"errors"
	"testing"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcvfs "github.com/kitsunium/sdk/internal/service/data/vfs"
)

// writeAtomicAllocRuns is how many publications the window counts, and as
// many run before it: enough that one allocating now and then still shows,
// few enough that their fsyncs stay well under a second.
const writeAtomicAllocRuns int = 50

// writeAtomicAllocBudget is what one uncontended publication may allocate on
// the window's stack (#813, at most 22 asked): os.Root's confinement — the
// guard's Lstat 3, the temporary's exclusive create 5, the rename 6, the
// directory's open for its fsync 4 — and the temporary's name, 1. Measured
// exactly that, run after run; BenchmarkWriteAtomic/os/256 reads 20, its
// count also taking a tiny allocation the heap profile cannot see. It was 32
// before #813: the temporary's name cost 5 (sixteen random bytes, their hex,
// path.Join), and the directory's commit group 8 even when no other
// publication shared it. The window counts only the caller's goroutine, so a
// flush moved back onto the group's own would leave the count and add the
// group's: TestAnUncontendedFlushRunsOnItsPublicationsGoroutine holds that.
const writeAtomicAllocBudget uint64 = 19

// TestAnUncontendedPublicationStaysWithinItsAllocationBudget holds
// WriteAtomic, alone on its directory, to its budget: the name built once on
// the stack, and the directory's flush through the commit group's idle path —
// on the caller's goroutine, so every allocation of the publication is on the
// window's stack and counted.
func TestAnUncontendedPublicationStaysWithinItsAllocationBudget(t *testing.T) {
	filesystem, err := svcvfs.NewOS(t.TempDir())
	if errors.Is(err, coreproc.UnsupportedPlatform) {
		t.Skip("NewOS refuses this platform: no disk publication to count")
	}
	if err != nil {
		t.Fatalf("NewOS = %v", err)
	}
	disk, ok := filesystem.(corevfs.FullFS)
	if !ok {
		t.Fatalf("NewOS returned %T, not a FullFS", filesystem)
	}
	payload := make([]byte, 256)
	publish := func() {
		if pErr := disk.WriteAtomic("published.bin", payload, 0o644); pErr != nil {
			t.Fatalf("WriteAtomic = %v", pErr)
		}
	}
	for range writeAtomicAllocRuns {
		publish()
	}
	got := mallocsOver(writeAtomicAllocRuns, publish)
	reportWindow(t, "WriteAtomic", got)
	if want := writeAtomicAllocBudget * uint64(writeAtomicAllocRuns); got.own > want {
		t.Fatalf("%d uncontended publications made %d allocations, want at most %d (%d a call)", writeAtomicAllocRuns, got.own, want, writeAtomicAllocBudget)
	}
	t.Logf("%d publications: %d allocations on the window's stack, %d process-wide", writeAtomicAllocRuns, got.own, got.process)
}
