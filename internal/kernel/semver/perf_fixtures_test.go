//go:build !race

// The fixtures of semver's performance contracts (design/sdk.yaml,
// budgets) — the allocation claim the package doc states: every reading is
// a substring of its input, found in one pass, so nothing here allocates.
// Each fixture sets one call up and returns it, and the perf_gen_test.go
// kit gen writes beside this file counts its allocations against its
// budget: the total over 30 000 calls after as many warm-up calls.
//
// It is the claim a hand-written parser loses first and no functional test
// sees: strings.Split in place of the in-place scan returns exactly the same
// identifiers, so every test in the package passes against it, and every
// comparison of two pre-releases then costs a slice. A version comparison sits
// in sort functions and in the loop that picks the newest of a release list;
// the price would be paid per comparison, silently.
//
// They were TestReadingsAllocateNothing (semver_alloc_external_test.go),
// bound 0, over inputs that reach every branch that could allocate: a
// pre-release of several identifiers on both sides of a comparison, build
// metadata, numbers past uint64, a shorthand, an invalid string, and each
// pseudo-version reading including the time. A function with several of
// those cases walks them all in its one call: a sum of zeros stays zero, and
// any case that allocates shows.
//
// MUTATION: replacing nextIdentifier's scan in compareIdentifierLists with
// strings.Split of both lists — the first thing anyone reaches for to walk
// dot-separated identifiers, and invisible to every functional test, since it
// returns the same identifiers — failed the old test at `Compare of two
// pre-releases: 500 calls performed 1000 allocations, want 0`, one slice per
// list per call.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so a malloc count under `-race`
// measures the detector. The allocation lane runs this package, through the
// section kit gen keeps of tools/alloc-lane-targets.txt (SDK-wide rule 12).
package semver_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/semver"
)

// perfIsValid is IsValid's fixture: a full version, and an invalid string.
func perfIsValid(tb testing.TB) func() {
	return func() {
		//: the reading is right as well as free: a wrong answer fails the fixture.
		if !semver.IsValid("v1.2.3-rc.1.beta-2+build.007.sha") {
			tb.Fatal("IsValid refused a valid version")
		}
		//: the reading is right as well as free: a wrong answer fails the fixture.
		if semver.IsValid("v1.2.3-01") {
			tb.Fatal("IsValid accepted a padded pre-release number")
		}
	}
}

// perfCompare is Compare's fixture: two pre-releases, numbers past uint64,
// and a shorthand against an invalid string.
func perfCompare(tb testing.TB) func() {
	return func() {
		//: the reading is right as well as free: a wrong answer fails the fixture.
		if semver.Compare("v1.0.0-beta.11.x", "v1.0.0-beta.2.y.z") != 1 {
			tb.Fatal("Compare put beta.11 below beta.2")
		}
		//: the reading is right as well as free: a wrong answer fails the fixture.
		if semver.Compare("v18446744073709551616.0.0", "v18446744073709551615.9.9+meta") != 1 {
			tb.Fatal("Compare misordered numbers past uint64")
		}
		//: the reading is right as well as free: a wrong answer fails the fixture.
		if semver.Compare("v2", "2.0.0") != 1 {
			tb.Fatal("Compare put an invalid string above a shorthand")
		}
	}
}

// perfPrerelease is Prerelease's fixture.
func perfPrerelease(tb testing.TB) func() {
	return func() {
		//: the reading is right as well as free: a wrong answer fails the fixture.
		if semver.Prerelease("v1.0.0-rc.1+build") != "-rc.1" {
			tb.Fatal("Prerelease did not return the pre-release")
		}
	}
}

// perfIsPseudoVersion is IsPseudoVersion's fixture.
func perfIsPseudoVersion(tb testing.TB) func() {
	return func() {
		//: the reading is right as well as free: a wrong answer fails the fixture.
		if !semver.IsPseudoVersion("v1.2.4-pre.0.20260924100234-23e4c32e7484+incompatible") {
			tb.Fatal("IsPseudoVersion refused a pseudo-version")
		}
	}
}

// perfPseudoVersionRev is PseudoVersionRev's fixture.
func perfPseudoVersionRev(tb testing.TB) func() {
	return func() {
		//: the reading is right as well as free: a wrong answer fails the fixture.
		if rev, ok := semver.PseudoVersionRev("v0.0.0-20260924095948-8cf38860b6ef"); !ok || rev != "8cf38860b6ef" {
			tb.Fatal("PseudoVersionRev did not read the revision")
		}
	}
}

// perfPseudoVersionTime is PseudoVersionTime's fixture.
func perfPseudoVersionTime(tb testing.TB) func() {
	return func() {
		//: the reading is right as well as free: a wrong answer fails the fixture.
		if _, ok := semver.PseudoVersionTime("v1.2.4-0.20260924100234-23e4c32e7484"); !ok {
			tb.Fatal("PseudoVersionTime did not read the time")
		}
	}
}
