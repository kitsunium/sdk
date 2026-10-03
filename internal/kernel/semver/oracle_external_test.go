package semver_test

import (
	"math/big"
	"regexp"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/semver"
)

// The oracles below are written from the documents, not from this package:
// what a hand-written parser gets subtly wrong is exactly what an independent
// reading catches. They are slow, allocate freely, and are used only here.
var (
	// fullPattern is the regular expression SemVer 2.0.0 itself publishes
	// (semver.org, "Is there a suggested regular expression"), with Go's
	// leading "v". Groups: 1 major, 2 minor, 3 patch, 4 pre-release without
	// its "-", 5 build metadata without its "+".
	fullPattern = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
		`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
		`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)
	// shortPattern is Go's two shorthands, vMAJOR and vMAJOR.MINOR, which
	// carry neither a pre-release nor build metadata. Groups: 1 major, 2 minor.
	shortPattern = regexp.MustCompile(`^v(0|[1-9]\d*)(?:\.(0|[1-9]\d*))?$`)
	// pseudoPattern is the Go toolchain's own pattern for a pseudo-version
	// (golang.org/x/mod/module, pseudo.go, pseudoVersionRE), applied — as the
	// toolchain applies it — together with validity and a count of at least
	// two hyphens.
	pseudoPattern = regexp.MustCompile(`^v[0-9]+\.(0\.0-|\d+\.\d+-([^+]*\.)?0\.)\d{14}-[A-Za-z0-9]+(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	// numericPattern is §9's numeric identifier: digits and nothing else.
	// Deciding it with big.Int.SetString instead would read "-5" — an
	// ALPHANUMERIC identifier, a hyphen being a non-digit — as minus five.
	numericPattern = regexp.MustCompile(`^[0-9]+$`)
)

// oracleVersion is a version as the oracle reads it: numbers as big integers,
// the pre-release as its list of identifiers and as written, and the build
// metadata as written.
type oracleVersion struct {
	numbers      [3]*big.Int
	prerelease   []string
	preWritten   string
	buildWritten string
}

// oracleParse reads v through the published patterns.
func oracleParse(v string) (oracleVersion, bool) {
	if m := shortPattern.FindStringSubmatch(v); m != nil {
		minor := m[2]
		if minor == "" {
			minor = "0"
		}
		return oracleVersion{numbers: [3]*big.Int{bigOf(m[1]), bigOf(minor), big.NewInt(0)}}, true
	}
	m := fullPattern.FindStringSubmatch(v)
	if m == nil {
		return oracleVersion{}, false
	}
	parsed := oracleVersion{numbers: [3]*big.Int{bigOf(m[1]), bigOf(m[2]), bigOf(m[3])}}
	if m[4] != "" {
		parsed.prerelease = strings.Split(m[4], ".")
		parsed.preWritten = "-" + m[4]
	}
	if m[5] != "" {
		parsed.buildWritten = "+" + m[5]
	}
	return parsed, true
}

// bigOf reads a string of digits the pattern has already vetted.
func bigOf(digits string) *big.Int {
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		panic("oracle: the pattern let through a number big.Int cannot read: " + digits)
	}
	return n
}

// oracleCompare is SemVer §11, read off the specification, over the oracle's
// parse; invalid strings rank below versions and tie with each other, the
// contract the package keeps from x/mod.
func oracleCompare(v, w string) int {
	a, okA := oracleParse(v)
	b, okB := oracleParse(w)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := range a.numbers {
		if order := a.numbers[i].Cmp(b.numbers[i]); order != 0 {
			return order
		}
	}
	switch {
	case len(a.prerelease) == 0 && len(b.prerelease) == 0:
		return 0
	case len(a.prerelease) == 0:
		return 1
	case len(b.prerelease) == 0:
		return -1
	}
	for i := 0; i < len(a.prerelease) && i < len(b.prerelease); i++ {
		if order := oracleIdentifier(a.prerelease[i], b.prerelease[i]); order != 0 {
			return order
		}
	}
	return sign(len(a.prerelease) - len(b.prerelease))
}

// oracleIdentifier is §11.4.1 to §11.4.3 for one pair of identifiers.
func oracleIdentifier(x, y string) int {
	numericX, numericY := numericPattern.MatchString(x), numericPattern.MatchString(y)
	switch {
	case numericX && numericY:
		return bigOf(x).Cmp(bigOf(y))
	case numericX:
		return -1
	case numericY:
		return 1
	default:
		return strings.Compare(x, y)
	}
}

// oraclePseudo is the toolchain's recognition of a pseudo-version.
func oraclePseudo(v string) bool {
	_, valid := oracleParse(v)
	return strings.Count(v, "-") >= 2 && valid && pseudoPattern.MatchString(v)
}

// seeds is every string the hand-written tables use, the starting point both
// of the mutation corpus and of the fuzz targets.
func seeds() []string {
	out := make([]string, 0, 2*len(vectors)+2*len(pseudoVectors))
	for _, v := range vectors {
		out = append(out, v.in)
	}
	for _, p := range pseudoVectors {
		out = append(out, p.older, p.version)
	}
	return append(out,
		"v0", "v0.0.0", "v18446744073709551616.0.0", "v1.0.0-0", "v1.0.0-00a",
		"v1.0.0+00.007", "v1.0.0--", "v1.0.0-rc.1+build.5",
		"v1.2.4-rc.1.0.20260924100234-23e4c32e7484",
		"v0.0.0-20061302150405-abcdef123456",
		"v2.0.1-0.20260924100234-23e4c32e7484+incompatible",
	)
}

// mutants returns every string one edit away from s — each byte deleted, each
// byte replaced, and a byte inserted at each position — over an alphabet of
// the characters the grammar gives a meaning to, plus a space and a byte that
// is not ASCII. Near misses are where a hand-written parser and its
// specification part ways.
func mutants(s string) []string {
	const alphabet = "019aZv-.+ \xff"
	out := make([]string, 0, len(s)*(2*len(alphabet)+1)+len(alphabet))
	for i := range len(s) {
		out = append(out, s[:i]+s[i+1:])
		for j := range len(alphabet) {
			out = append(out, s[:i]+alphabet[j:j+1]+s[i+1:])
		}
	}
	for i := range len(s) + 1 {
		for j := range len(alphabet) {
			out = append(out, s[:i]+alphabet[j:j+1]+s[i:])
		}
	}
	return out
}

// TestAgreesWithTheSpecificationOnEveryNearMiss runs every mutant of every
// seed — tens of thousands of strings, most of them one character away from a
// version — through the package and through the oracles: validity against the
// pattern SemVer 2.0.0 publishes, pseudo-version recognition against the
// toolchain's pattern, and precedence against a comparison written from §11
// over big integers. A disagreement names the string.
func TestAgreesWithTheSpecificationOnEveryNearMiss(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	checked, valid, pseudo := 0, 0, 0
	for _, seed := range seeds() {
		for _, v := range append(mutants(seed), seed) {
			if seen[v] {
				continue
			}
			seen[v] = true
			checked++
			parsed, wantValid := oracleParse(v)
			if got := semver.IsValid(v); got != wantValid {
				t.Errorf("IsValid(%q) = %v, the specification's pattern says %v", v, got, wantValid)
			}
			if got := semver.Prerelease(v); got != parsed.preWritten {
				t.Errorf("Prerelease(%q) = %q, the specification's pattern says %q", v, got, parsed.preWritten)
			}
			if wantValid {
				valid++
			}
			wantPseudo := oraclePseudo(v)
			if got := semver.IsPseudoVersion(v); got != wantPseudo {
				t.Errorf("IsPseudoVersion(%q) = %v, the toolchain's pattern says %v", v, got, wantPseudo)
			}
			if wantPseudo {
				pseudo++
			}
			if got, want := semver.Compare(v, seed), oracleCompare(v, seed); got != want {
				t.Errorf("Compare(%q, %q) = %d, §11 says %d", v, seed, got, want)
			}
		}
	}
	//: a corpus with no versions in it, or no pseudo-versions, would agree
	//: with anything; say how much of each it held.
	if valid < 1000 || pseudo < 100 {
		t.Errorf("the corpus held %d strings, %d valid and %d pseudo-versions: too few to mean anything",
			checked, valid, pseudo)
	}
	t.Logf("%d distinct strings: %d valid, %d pseudo-versions", checked, valid, pseudo)
}

// TestCompareIsATotalOrderOverTheSeeds checks, over every pair and every
// triple of the seeds, that Compare agrees with §11 and is antisymmetric and
// transitive — the properties slices.SortFunc assumes of a comparison.
func TestCompareIsATotalOrderOverTheSeeds(t *testing.T) {
	t.Parallel()
	list := seeds()
	for _, a := range list {
		for _, b := range list {
			ab := semver.Compare(a, b)
			if want := oracleCompare(a, b); ab != want {
				t.Errorf("Compare(%q, %q) = %d, §11 says %d", a, b, ab, want)
			}
			if ba := semver.Compare(b, a); ab != -ba {
				t.Errorf("Compare(%q, %q) = %d but Compare(%q, %q) = %d", a, b, ab, b, a, ba)
			}
			for _, c := range list {
				if ab <= 0 && semver.Compare(b, c) <= 0 && semver.Compare(a, c) > 0 {
					t.Errorf("%q <= %q <= %q, yet Compare(%q, %q) > 0", a, b, c, a, c)
				}
			}
		}
	}
}

// FuzzIsValid holds validity to the specification's pattern on any input.
func FuzzIsValid(f *testing.F) {
	for _, s := range seeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		_, want := oracleParse(v)
		if got := semver.IsValid(v); got != want {
			t.Errorf("IsValid(%q) = %v, the specification's pattern says %v", v, got, want)
		}
	})
}

// FuzzCompare holds precedence to §11 and to antisymmetry on any pair.
func FuzzCompare(f *testing.F) {
	list := seeds()
	for i, s := range list {
		f.Add(s, list[(i*7+3)%len(list)])
	}
	f.Fuzz(func(t *testing.T, v, w string) {
		got := semver.Compare(v, w)
		if want := oracleCompare(v, w); got != want {
			t.Errorf("Compare(%q, %q) = %d, §11 says %d", v, w, got, want)
		}
		if back := semver.Compare(w, v); back != -got {
			t.Errorf("Compare(%q, %q) = %d but Compare(%q, %q) = %d", v, w, got, w, v, back)
		}
	})
}

// FuzzReadings holds every reading to what it promises about any input: the
// pre-release is the one the specification's pattern captures — "" for a
// release and for a string that is no version — and the pseudo-version
// readings agree with the toolchain's pattern and with each other.
func FuzzReadings(f *testing.F) {
	for _, s := range seeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		parsed, _ := oracleParse(v)
		if got := semver.Prerelease(v); got != parsed.preWritten {
			t.Errorf("Prerelease(%q) = %q, the specification's pattern says %q", v, got, parsed.preWritten)
		}
		pseudo := semver.IsPseudoVersion(v)
		if want := oraclePseudo(v); pseudo != want {
			t.Errorf("IsPseudoVersion(%q) = %v, the toolchain's pattern says %v", v, pseudo, want)
		}
		rev, revOK := semver.PseudoVersionRev(v)
		if _, timeOK := semver.PseudoVersionTime(v); revOK != pseudo || (timeOK && !pseudo) {
			t.Errorf("readings of %q disagree: IsPseudoVersion %v, revision %v, time %v", v, pseudo, revOK, timeOK)
		}
		if revOK && !strings.HasSuffix(strings.TrimSuffix(v, parsed.buildWritten), "-"+rev) {
			t.Errorf("PseudoVersionRev(%q) = %q, which does not end the version", v, rev)
		}
	})
}
