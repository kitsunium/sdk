package semver_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/semver"
)

// Inputs the benchmarks read. compareLeft and compareRight are the pair
// golang.org/x/mod/semver's own BenchmarkCompare uses — equal in precedence,
// so every field is read — which is what makes the two figures in BENCH.md
// comparable.
const (
	compareLeft     string = "v1.0.0+metadata-dash"
	compareRight    string = "v1.0.0+metadata-dash1"
	releaseLeft     string = "v1.9.12"
	releaseRight    string = "v1.10.0"
	prereleaseLeft  string = "v1.0.0-beta.11"
	prereleaseRight string = "v1.0.0-beta.2"
	fullVersion     string = "v1.2.3-rc.1+build.5"
	pseudoVersion   string = "v1.2.4-0.20260924100234-23e4c32e7484"
)

// BenchmarkCompare is x/mod's own benchmark, on x/mod's own input.
func BenchmarkCompare(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if semver.Compare(compareLeft, compareRight) != 0 {
			b.Fatal("Compare() of two versions differing only in build metadata is not 0")
		}
	}
}

// BenchmarkCompareReleases is the common case: two releases, decided by the
// minor number, which a string comparison would get backwards.
func BenchmarkCompareReleases(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if semver.Compare(releaseLeft, releaseRight) != -1 {
			b.Fatal("Compare() put v1.10.0 below v1.9.12")
		}
	}
}

// BenchmarkComparePrereleases walks the pre-release identifiers to the last,
// where a numeric comparison decides 11 > 2.
func BenchmarkComparePrereleases(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if semver.Compare(prereleaseLeft, prereleaseRight) != 1 {
			b.Fatal("Compare() put beta.11 below beta.2")
		}
	}
}

// BenchmarkIsValid parses a version carrying every part.
func BenchmarkIsValid(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if !semver.IsValid(fullVersion) {
			b.Fatal("IsValid() refused a valid version")
		}
	}
}

// BenchmarkPrerelease parses a version carrying every part and returns its
// pre-release, a substring of the input.
func BenchmarkPrerelease(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if semver.Prerelease(fullVersion) != "-rc.1" {
			b.Fatal("Prerelease() did not return the pre-release")
		}
	}
}

// BenchmarkIsPseudoVersion recognises the commonest pseudo-version shape.
func BenchmarkIsPseudoVersion(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if !semver.IsPseudoVersion(pseudoVersion) {
			b.Fatal("IsPseudoVersion() refused a pseudo-version")
		}
	}
}

// BenchmarkPseudoVersionTime recognises the pseudo-version and reads its time.
func BenchmarkPseudoVersionTime(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := semver.PseudoVersionTime(pseudoVersion); !ok {
			b.Fatal("PseudoVersionTime() did not read the time")
		}
	}
}
