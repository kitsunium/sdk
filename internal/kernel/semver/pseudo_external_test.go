package semver_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/semver"
)

// pseudoTime is the commit time every pseudo-version vector carries.
var pseudoTime = time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC)

// pseudoVectors are the pairs golang.org/x/mod/module pins in its own suite
// (pseudo_test.go, `pseudoTests`): a pseudo-version and the version it was
// built on — every shape of the Go modules reference, with and without build
// metadata, a base past uint64, and a base whose pre-release is a bare hyphen.
// x/mod lists v0.0.0-20060102150405-hash twice, once per spelling of the major
// version it asks its constructor for; this package builds no pseudo-version,
// so the row appears once. Each pseudo-version reads as revision "hash" at
// pseudoTime; no base reads as a pseudo-version at all, the empty string
// included.
var pseudoVectors = []struct {
	older   string
	version string
}{
	{older: "", version: "v0.0.0-20060102150405-hash"},
	{older: "", version: "v1.0.0-20060102150405-hash"},
	{older: "", version: "v2.0.0-20060102150405-hash"},
	{older: "v0.0.0", version: "v0.0.1-0.20060102150405-hash"},
	{older: "v1.2.3", version: "v1.2.4-0.20060102150405-hash"},
	{older: "v1.2.99999999999999999", version: "v1.2.100000000000000000-0.20060102150405-hash"},
	{older: "v1.2.3-pre", version: "v1.2.3-pre.0.20060102150405-hash"},
	{older: "v1.3.0-pre", version: "v1.3.0-pre.0.20060102150405-hash"},
	{older: "v0.0.0--", version: "v0.0.0--.0.20060102150405-hash"},
	{older: "v1.0.0+metadata", version: "v1.0.1-0.20060102150405-hash+metadata"},
	{older: "v2.0.0+incompatible", version: "v2.0.1-0.20060102150405-hash+incompatible"},
	{older: "v2.3.0-pre+incompatible", version: "v2.3.0-pre.0.20060102150405-hash+incompatible"},
}

// TestPseudoVersionVectors reads every vector both ways: the pseudo-version is
// recognised and yields its revision and time, the version it was built on
// yields neither.
func TestPseudoVersionVectors(t *testing.T) {
	t.Parallel()
	for _, c := range pseudoVectors {
		if !semver.IsPseudoVersion(c.version) {
			t.Errorf("IsPseudoVersion(%q) = false, want true", c.version)
		}
		if rev, ok := semver.PseudoVersionRev(c.version); rev != "hash" || !ok {
			t.Errorf("PseudoVersionRev(%q) = %q, %v; want %q, true", c.version, rev, ok, "hash")
		}
		if at, ok := semver.PseudoVersionTime(c.version); !at.Equal(pseudoTime) || at.Location() != time.UTC || !ok {
			t.Errorf("PseudoVersionTime(%q) = %v, %v; want %v, true", c.version, at, ok, pseudoTime)
		}
		if semver.IsPseudoVersion(c.older) {
			t.Errorf("IsPseudoVersion(%q) = true, want false", c.older)
		}
		if rev, ok := semver.PseudoVersionRev(c.older); rev != "" || ok {
			t.Errorf("PseudoVersionRev(%q) = %q, %v; want \"\", false", c.older, rev, ok)
		}
		if at, ok := semver.PseudoVersionTime(c.older); !at.IsZero() || ok {
			t.Errorf("PseudoVersionTime(%q) = %v, %v; want the zero time, false", c.older, at, ok)
		}
	}
}

// TestPseudoVersionShapes pins the boundary of the grammar: where the stamp
// may sit, how long its time is, what its revision may hold, and what may
// follow it. Every expectation agrees with the toolchain's own pattern, which
// TestPseudoVersionAgreesWithTheToolchainPattern checks mechanically.
func TestPseudoVersionShapes(t *testing.T) {
	t.Parallel()
	type tc struct {
		in     string
		pseudo bool
	}
	tests := []tc{
		//: the three shapes, real-world spellings.
		{in: "v0.0.0-20260924095948-8cf38860b6ef", pseudo: true},
		{in: "v1.2.4-0.20260924100234-23e4c32e7484", pseudo: true},
		{in: "v1.2.4-rc.1.0.20260924100234-23e4c32e7484", pseudo: true},
		{in: "v2.0.1-0.20260924100234-23e4c32e7484+incompatible", pseudo: true},
		//: x/mod's malformed-base vectors: nonsense as a base, a pseudo-version
		//: by shape.
		{in: "v0.0.0-0.20060102150405-hash", pseudo: true},
		{in: "v0.1.0-0.20060102150405-hash", pseudo: true},
		{in: "v1.0.0-0.20060102150405-hash", pseudo: true},
		{in: "v0.0.0-20060102150405-hash+incompatible", pseudo: true},
		{in: "v0.0.0-20060102150405-hash+metadata", pseudo: true},
		{in: "v0.0.0-", pseudo: false},
		{in: "v0.0.0", pseudo: false},
		//: the stamp alone belongs to vX.0.0 only.
		{in: "v1.2.3-20060102150405-hash", pseudo: false},
		{in: "v0.1.0-20060102150405-hash", pseudo: false},
		{in: "v0.0.1-20060102150405-hash", pseudo: false},
		//: otherwise the identifier before it is exactly 0.
		{in: "v1.2.3-1.20060102150405-hash", pseudo: false},
		{in: "v1.2.3-x0.20060102150405-hash", pseudo: false},
		{in: "v1.2.3-pre.00.20060102150405-hash", pseudo: false},
		//: the time is exactly fourteen digits.
		{in: "v0.0.0-2006010215040-hash", pseudo: false},
		{in: "v0.0.0-200601021504055-hash", pseudo: false},
		{in: "v0.0.0-2006010215040x-hash", pseudo: false},
		//: the revision is one or more letters and digits, and comes last.
		{in: "v0.0.0-20060102150405-", pseudo: false},
		{in: "v0.0.0-20060102150405-ha-sh", pseudo: false},
		{in: "v0.0.0-20060102150405-hash.x", pseudo: false},
		{in: "v0.0.0-20060102150405-HASH0", pseudo: true},
		//: a shorthand, a release, and things that are not versions.
		{in: "v1", pseudo: false},
		{in: "v1.2.3", pseudo: false},
		{in: "", pseudo: false},
		{in: "---", pseudo: false},
		{in: "0.0.0-20060102150405-hash", pseudo: false},
		{in: "v0.0.0-20060102150405-hash+", pseudo: false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := semver.IsPseudoVersion(c.in); got != c.pseudo {
			t.Errorf("IsPseudoVersion(%q) = %v, want %v", c.in, got, c.pseudo)
		}
		//: the revision reads exactly when the shape is recognised.
		if _, ok := semver.PseudoVersionRev(c.in); ok != c.pseudo {
			t.Errorf("PseudoVersionRev(%q) ok = %v, want %v", c.in, ok, c.pseudo)
		}
	}
	for _, c := range tests {
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPseudoVersionTimeThatIsNoInstant pins the one reading that can fail on
// a recognised pseudo-version: the shape is right, so the revision reads, but
// a thirteenth month is no time, so the time does not. x/mod answered the same
// with an error; this answers with false.
func TestPseudoVersionTimeThatIsNoInstant(t *testing.T) {
	t.Parallel()
	const thirteenthMonth = "v0.0.0-20061302150405-abcdef123456"
	if !semver.IsPseudoVersion(thirteenthMonth) {
		t.Fatalf("IsPseudoVersion(%q) = false: the shape is a pseudo-version's", thirteenthMonth)
	}
	if rev, ok := semver.PseudoVersionRev(thirteenthMonth); rev != "abcdef123456" || !ok {
		t.Errorf("PseudoVersionRev(%q) = %q, %v; want %q, true", thirteenthMonth, rev, ok, "abcdef123456")
	}
	if at, ok := semver.PseudoVersionTime(thirteenthMonth); ok || !at.IsZero() {
		t.Errorf("PseudoVersionTime(%q) = %v, %v; want the zero time, false", thirteenthMonth, at, ok)
	}
	//: x/mod's own "---", which is not even a version.
	if at, ok := semver.PseudoVersionTime("---"); ok || !at.IsZero() {
		t.Errorf("PseudoVersionTime(%q) = %v, %v; want the zero time, false", "---", at, ok)
	}
}

// TestPseudoVersionsOrderBetweenTheirTags pins why the shapes are what they
// are: a pseudo-version built on a tag orders after that tag and below the
// next version a tag could name, including that version's pre-releases.
func TestPseudoVersionsOrderBetweenTheirTags(t *testing.T) {
	t.Parallel()
	type tc struct {
		name                 string
		below, pseudo, above string
	}
	tests := []tc{
		{name: "after a release", below: "v1.2.3", pseudo: "v1.2.4-0.20060102150405-hash", above: "v1.2.4-alpha"},
		{name: "after a pre-release", below: "v1.2.3-pre", pseudo: "v1.2.3-pre.0.20060102150405-hash", above: "v1.2.3-pre.1"},
		{name: "before any tag", below: "v0.0.0-0", pseudo: "v0.0.0-20060102150405-hash", above: "v0.0.0"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if semver.Compare(c.below, c.pseudo) != -1 || semver.Compare(c.pseudo, c.above) != -1 {
			t.Errorf("want %q < %q < %q; got %d and %d", c.below, c.pseudo, c.above,
				semver.Compare(c.below, c.pseudo), semver.Compare(c.pseudo, c.above))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
