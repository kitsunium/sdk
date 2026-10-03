//go:build !race

// Package semver_test — the allocation contract the package doc states:
// every reading is a substring of its input, found in one pass, so nothing
// here allocates.
//
// It is the claim a hand-written parser loses first and no functional test
// sees: strings.Split in place of the in-place scan returns exactly the same
// identifiers, so every test in the package passes against it, and every
// comparison of two pre-releases then costs a slice. A version comparison sits
// in sort functions and in the loop that picks the newest of a release list;
// the price would be paid per comparison, silently.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so a malloc count under `-race`
// measures the detector. That makes this file invisible to the race suite,
// which is why //internal/kernel/semver:semver_test carries an entry in
// tools/alloc-lane-targets.txt — the race-off alloc lane is its ONLY gate
// (SDK-wide rule 12).
package semver_test

import (
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/semver"
)

// allocRuns is how many times each claim is exercised: enough that an
// allocation made on only some calls has happened several times by the end.
const allocRuns int = 500

// mallocsOver reports the TOTAL number of heap allocations f performs across
// runs calls — the bookkeeping of internal/service/writer/levelgate's gate,
// which records why: testing.AllocsPerRun divides as integers, so a defect
// allocating less than once per call reports exactly 0. GOMAXPROCS is pinned
// so no other P allocates into the count, collection is held off for the
// window, and one call warms up first.
func mallocsOver(runs int, f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	f()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	return after.Mallocs - before.Mallocs
}

// TestReadingsAllocateNothing pins the package doc's cost claim on every
// function, over inputs that reach every branch that could allocate: a
// pre-release of several identifiers on both sides of a comparison, build
// metadata, numbers past uint64, a shorthand, an invalid string, and each
// pseudo-version reading including the time.
//
// MUTATION: replacing nextIdentifier's scan in compareIdentifierLists with
// strings.Split of both lists — the first thing anyone reaches for to walk
// dot-separated identifiers, and invisible to every functional test, since it
// returns the same identifiers — fails at
// `Compare of two pre-releases: 500 calls performed 1000 allocations, want 0`,
// one slice per list per call. Reproduced identically across repeated runs.
func TestReadingsAllocateNothing(t *testing.T) {
	type tc struct {
		name string
		call func()
	}
	tests := []tc{
		{name: "IsValid of a full version", call: func() {
			if !semver.IsValid("v1.2.3-rc.1.beta-2+build.007.sha") {
				t.Fatal("IsValid refused a valid version")
			}
		}},
		{name: "IsValid of an invalid string", call: func() {
			if semver.IsValid("v1.2.3-01") {
				t.Fatal("IsValid accepted a padded pre-release number")
			}
		}},
		{name: "Compare of two pre-releases", call: func() {
			if semver.Compare("v1.0.0-beta.11.x", "v1.0.0-beta.2.y.z") != 1 {
				t.Fatal("Compare put beta.11 below beta.2")
			}
		}},
		{name: "Compare past uint64", call: func() {
			if semver.Compare("v18446744073709551616.0.0", "v18446744073709551615.9.9+meta") != 1 {
				t.Fatal("Compare misordered numbers past uint64")
			}
		}},
		{name: "Compare of a shorthand and an invalid string", call: func() {
			if semver.Compare("v2", "2.0.0") != 1 {
				t.Fatal("Compare put an invalid string above a shorthand")
			}
		}},
		{name: "Prerelease", call: func() {
			if semver.Prerelease("v1.0.0-rc.1+build") != "-rc.1" {
				t.Fatal("Prerelease did not return the pre-release")
			}
		}},
		{name: "IsPseudoVersion", call: func() {
			if !semver.IsPseudoVersion("v1.2.4-pre.0.20260924100234-23e4c32e7484+incompatible") {
				t.Fatal("IsPseudoVersion refused a pseudo-version")
			}
		}},
		{name: "PseudoVersionRev", call: func() {
			if rev, ok := semver.PseudoVersionRev("v0.0.0-20260924095948-8cf38860b6ef"); !ok || rev != "8cf38860b6ef" {
				t.Fatal("PseudoVersionRev did not read the revision")
			}
		}},
		{name: "PseudoVersionTime", call: func() {
			if _, ok := semver.PseudoVersionTime("v1.2.4-0.20260924100234-23e4c32e7484"); !ok {
				t.Fatal("PseudoVersionTime did not read the time")
			}
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := mallocsOver(allocRuns, c.call); got != 0 {
			t.Errorf("%s: %d calls performed %d allocations, want 0", c.name, allocRuns, got)
		}
	}
	//: sequential on purpose: mallocsOver pins GOMAXPROCS and holds off the
	//: collector, both process-wide.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
