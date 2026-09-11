//go:build !race

// Package token — the allocation contract the CVE-2025-30204 defence rests on.
//
// splitCompact is the first function that touches an unauthenticated token,
// and two things are claimed about it in three places — its own doc comment,
// encoding.go's segmentsValue comment, and the package CLAUDE.md §Bounds:
//
//  1. splitting a well-formed token "allocates nothing at all", because the
//     parts are views into the caller's string held in a fixed [4]string array
//     rather than a slice a separator count can grow; and
//  2. refusing an oversized token costs the same whatever its size, because
//     the length bound is checked BEFORE the scan.
//
// Neither had a gate. Both are the same defect if they regress — CVE-2025-30204
// was a strings.Split on the token before any length check, so a few megabytes
// of "." became hundreds of megabytes of slice headers — and a regression to
// that shape is invisible to every functional test in this package, because
// strings.Split returns exactly the same parts.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so any malloc count under `-race`
// measures the detector. That makes this file invisible to the race suite,
// which is why //internal/service/token:token_test carries an entry in
// tools/alloc-lane-targets.txt — the race-off alloc lane is its ONLY gate
// (SDK-wide rule 12).
package token

import (
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	// allocRuns is how many times each claim is exercised. Large enough that
	// an amortised allocation — one that happens on a growth step rather than
	// on every call — has happened several times by the end.
	allocRuns int = 500
	// hostileSize is the oversized token the second test presents. It is
	// 1024x DefaultMaxTokenLen, so an implementation whose cost tracks the
	// input cannot hide inside the noise of one that does not.
	hostileSize int = 8 << 20
	// hostileFill is the byte the oversized tokens are made of. It is NOT a
	// separator, deliberately: see TestOversizedRefusalDoesNotScanTheToken.
	hostileFill string = "a"
	// timingSamples is how many independent batches medianNanos takes. Odd,
	// so the median is a real sample.
	timingSamples int = 9
	// timingRuns is how many calls each batch makes.
	timingRuns int64 = 64
	// maxRefusalRatio is the largest cost ratio between the 8 MiB refusal and
	// the 8 KiB one that this test accepts. The clean ratio measures 0.9-1.4
	// and the scan-first mutation measures 553-1364, so 50 sits an order of
	// magnitude clear of both.
	maxRefusalRatio float64 = 50
	// allocValidToken is a well-formed three-segment compact token. Its
	// content is irrelevant to splitCompact, which only looks for separators —
	// but a real shape keeps a future reader from assuming the test is about a
	// special case.
	allocValidToken string = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJpc3MiOiJodHRwczovL2F1dGguZXhhbXBsZSIsInN1YiI6InUiLCJleHAiOjE4OTM0NTYwMDB9." +
		"c2lnbmF0dXJlLXBsYWNlaG9sZGVyLXRoaXJ0eS10d28tb2N0ZXRz"
)

// mallocsOver reports the TOTAL number of heap allocations f performs across
// runs calls, rather than the per-call average.
//
// testing.AllocsPerRun is deliberately not used, and the reason is a mutation
// that PASSED against it elsewhere in this repository: its last line is
// `float64(mallocs / uint64(runs))`, an INTEGER division, so any defect
// allocating less than once per call reports exactly 0.0. A total is not
// subject to that rounding. The bookkeeping mirrors AllocsPerRun's otherwise —
// pin GOMAXPROCS so no other P allocates into the count, warm up so
// lazily-initialised state (including the runtime's interface-switch caches)
// is not attributed to the loop, and read the counter either side.
//
// It is a verbatim sibling of the helper in internal/service/writer/levelgate;
// the two are deliberately not shared, because a package's allocation gate
// must not be able to fail for a reason that lives in another package.
func mallocsOver(runs int, f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	//: collection is held off for the window: a collection is not free of
	//: allocations from the measured goroutine's point of view. The argument
	//: is evaluated now and the previous rate restored on return. NOT
	//: runtime.GC(), which returns before its sweep finishes and therefore
	//: allocates INSIDE the window it was meant to clear.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	//: warm up so first-call initialisation is not counted as steady state.
	f()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	//: Mallocs is cumulative and monotonic, so the difference is the total.
	return after.Mallocs - before.Mallocs
}

// TestSplitCompactAllocatesNothing pins claim 1: splitting a well-formed token
// performs ZERO heap allocations, because segmentsValue is a fixed array of
// views and not a slice grown per separator.
//
// MUTATION (2026-09-10). splitCompact's body was replaced by the shape
// CVE-2025-30204 describes:
//
//	parts := strings.Split(tok, ".")
//	if len(parts) > maxSegments { return segmentsValue{}, coretoken.Malformed }
//	for i, p := range parts { seg.part[i] = p }
//	seg.count = len(parts)
//
// Observed: `split of a well-formed token performed 500 allocations in 500
// calls, want 0` — one slice header array per call. Restored; `git diff`
// reports the file byte-identical and the test passes at 0.
func TestSplitCompactAllocatesNothing(t *testing.T) {
	got := mallocsOver(allocRuns, func() {
		seg, err := splitCompact(allocValidToken, DefaultMaxTokenLen)
		//: an error here would mean the loop is measuring the refusal path.
		if err != nil {
			t.Fatalf("splitCompact refused a well-formed token: %v", err)
		}
		sinkSegments = seg
	})
	//: zero, not "small": the parts are views, so there is nothing to allocate.
	if got != 0 {
		t.Fatalf("split of a well-formed token performed %d allocations in %d calls, want 0",
			got, allocRuns)
	}
}

// TestOversizedRefusalDoesNotScanTheToken pins claim 2: the length bound is
// checked BEFORE the scan, so refusing an oversized token costs the same
// whether it is one byte over the bound or a thousand times over it.
//
// TIME, not allocations, and that is the finding rather than a preference. The
// first draft of this test asserted that the malloc total for an 8 MiB refusal
// equalled the one for an 8 KiB refusal, and the mutation that moves the bound
// below the scan PASSED against it: the walk is strings.IndexByte into a fixed
// [4]string array, so scanning eight megabytes allocates exactly nothing. The
// guard was measuring an instrument the defect does not move.
//
// The INPUT is the second half of the same finding. The obvious hostile token
// — megabytes of "." — does not force a scan either, because the "more parts
// than the format allows" check short-circuits at the fourth separator, three
// bytes in, with or without the length bound. The shape that forces the scan
// is a long token with NO separator at all: strings.IndexByte then reads every
// byte before reporting that there are none. So the two bounds in splitCompact
// stop two different hostile shapes, and only one of them is CVE-2025-30204's.
//
// The threshold has more than an order of magnitude of headroom on both
// sides, which is why a timing assertion is sound here: measured on this box,
// the clean ratio is 0.9-1.4 and the mutated ratio is 553-1364.
//
// MUTATION (2026-09-10). The `if len(tok) > maxLen` block was moved out of the
// top of splitCompact and duplicated into the two return paths inside the
// walk, so the scan runs first and the verdict is unchanged. Observed, in
// three consecutive runs, the ratio the failure names: 1364x, then 553x, then
// 1334x, each against the 50x limit and each reported as `the length bound is
// no longer checked before the scan`. Restored; `git diff` reports encoding.go
// byte-identical and the ratio back inside the limit.
func TestOversizedRefusalDoesNotScanTheToken(t *testing.T) {
	small := strings.Repeat(hostileFill, DefaultMaxTokenLen+1)
	hostile := strings.Repeat(hostileFill, hostileSize)
	//: the same closure over two inputs, so nothing but the length differs.
	refuse := func(tok string) func() {
		return func() {
			seg, err := splitCompact(tok, DefaultMaxTokenLen)
			//: acceptance here would mean the loop is measuring the happy path.
			if err == nil {
				t.Fatal("splitCompact accepted an oversized token")
			}
			sinkSegments = seg
		}
	}
	ratio := float64(medianNanos(refuse(hostile))) / float64(medianNanos(refuse(small)))
	//: a bound checked after the scan makes this ratio track the size ratio,
	//: which is 1024x here.
	if ratio > maxRefusalRatio {
		t.Fatalf("refusing %d bytes cost %.0fx refusing %d bytes (limit %.0fx):"+
			" the length bound is no longer checked before the scan",
			hostileSize, ratio, DefaultMaxTokenLen+1, maxRefusalRatio)
	}
}

// medianNanos reports the median per-call wall time of f over timingSamples
// independent batches.
//
// A median rather than a mean, because the failure mode this defuses is one
// batch descheduled by the host — a VM whose balloon the hypervisor is
// reclaiming produces exactly that, and it moves a mean and not a median.
func medianNanos(f func()) int64 {
	//: warm up so first-call initialisation lands outside every batch.
	f()
	samples := make([]int64, timingSamples)
	for sample := range timingSamples {
		start := time.Now()
		for range timingRuns {
			f()
		}
		samples[sample] = int64(time.Since(start)) / timingRuns
	}
	slices.Sort(samples)
	//: an odd sample count, so this is a real element and not an average.
	return samples[timingSamples/2]
}
