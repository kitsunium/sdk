// Package client — the proof that the allocation-free path scans decide exactly
// what the strings.ToLower spelling they replaced decided.
//
// hasEncodedSeparator and hasDotSegment used to lowercase their input and
// compare. That allocated a copy of the whole path, and of every segment, for
// any request carrying a single uppercase byte — which url.URL.EscapedPath
// guarantees on a percent-encoded path and which a canonical UUID carries on a
// path that is not adversarial at all. They now fold ASCII in place.
//
// A path check is a security boundary, so "equivalent" is not asserted here, it
// is demonstrated three ways: the lemma the fold rests on is checked over the
// WHOLE Unicode code space, the two implementations are compared over the
// adversarial corpus by hand, and then over randomly assembled paths.
package client

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// comparedBytes is every ASCII byte the two scans compare an input against:
// the "%2e" / "%2f" / "%5c" triplets and the bare "." of a literal dot segment.
const comparedBytes string = ".%2efc5"

// referenceHasEncodedSeparator is the implementation hasEncodedSeparator
// replaced, kept verbatim so equivalence is measured against the real thing
// rather than against a paraphrase of it.
func referenceHasEncodedSeparator(escapedPath string) bool {
	lowered := strings.ToLower(escapedPath)
	return strings.Contains(lowered, "%2f") || strings.Contains(lowered, "%5c")
}

// referenceHasDotSegment is the implementation hasDotSegment replaced, kept
// verbatim for the same reason.
func referenceHasDotSegment(escapedPath string) bool {
	for seg := range strings.SplitSeq(strings.TrimPrefix(escapedPath, "/"), "/") {
		switch strings.ToLower(seg) {
		case ".", "..", "%2e", "%2e%2e", ".%2e", "%2e.":
			return true
		}
	}
	return false
}

// Test_noCodePointFoldsIntoAComparedByte is the LEMMA the whole rewrite rests
// on, and the only part of it that is not obvious.
//
// Folding ASCII in place is equivalent to strings.ToLower only if no code point
// outside ASCII can lowercase INTO one of the bytes the comparison looks at. If
// some rune did, a path carrying it would fold differently under the two
// spellings — the old one could see a "%2e" the new one cannot, or the reverse.
// The whole code space is swept rather than sampled, because a single
// counter-example is a path-traversal bypass and there is no reason to guess
// which plane it would live in.
//
// MUTATION: widening comparedBytes to include "i" — U+0130 LATIN CAPITAL LETTER
// I WITH DOT ABOVE lowercases to a bare ASCII "i" — fails with
// `U+0130 "İ" lowercases to "i", which contains a byte the scans compare
// against`. That is the shape of the counter-example this test exists to find,
// and it confirms the sweep reaches beyond the Latin-1 range.
func Test_noCodePointFoldsIntoAComparedByte(t *testing.T) {
	t.Parallel()
	var r rune
	for r = utf8.RuneSelf; r <= unicode.MaxRune; r++ {
		//: the surrogate range is not a code point and string(r) yields U+FFFD.
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		folded := strings.ToLower(string(r))
		//: any ASCII byte in the folded form must be outside the compared set.
		for i := range len(folded) {
			//: a byte at or above RuneSelf cannot equal an ASCII constant.
			if folded[i] >= utf8.RuneSelf {
				continue
			}
			//: an ASCII byte the comparison looks at would break the fold.
			if strings.IndexByte(comparedBytes, folded[i]) >= 0 {
				t.Fatalf("U+%04X %q lowercases to %q, which contains a byte the scans compare against",
					r, string(r), folded)
			}
		}
	}
}

// adversarialPaths is the corpus a path check has to survive: every spelling of
// a dot segment, every case of an encoded separator, and the ordinary shapes
// that must NOT be refused.
var adversarialPaths = []string{
	"", "/", "//", "///",
	"/v1/profiles",
	"/v1/profiles/",
	"/V1/PROFILES",
	"/v1/users/9F8E7D6C-1234-4ABC-9DEF-0123456789AB/orders",
	".", "..", "/.", "/..", "/./", "/../", "/a/./b", "/a/../b",
	"/%2e", "/%2E", "/%2e%2e", "/%2E%2E", "/%2e%2E", "/%2E%2e",
	"/.%2e", "/.%2E", "/%2e.", "/%2E.",
	"/a/%2e/b", "/a/%2E%2E/b", "/a/.%2E/b", "/a/%2E./b",
	"/%2f", "/%2F", "/a%2fb", "/a%2Fb", "/a%2fB", "/A%2FB",
	"/%5c", "/%5C", "/a%5cb", "/a%5Cb",
	"/a%2", "/a%", "%", "%2", "%2f", "%2F", "%5c",
	"/%252e", "/%252f", "/%%2f", "/%2ff", "/%2fF",
	"/a%2e", "/%2ea", "/%2e%2ea", "/a%2e%2e",
	"/" + strings.Repeat("a", 4096),
	"/" + strings.Repeat("%2E", 512),
	"/" + strings.Repeat("A", 300) + "/%2E%2E",
	"/a//b", "/a///b", "/a/ /b", "/a/\t/b",
	"/a/İ/b", "/a/K/b", "/a/É/b", "/a/é/b", "/a/日本/b",
	"/a/\xff\xfe/b", "/\xff", "/%2\xff", "/a/\xc3/b",
	"/a/%2eİ/b", "/İ%2e",
	"v1/profiles", "v1/../profiles",
}

// Test_pathScansMatchTheirReferenceOnTheCorpus compares the two spellings over
// every path a reviewer would think to try, including the ones that must be
// ADMITTED — an equivalence test that only carried refusals would pass for an
// implementation that refused everything.
//
// MUTATION: making lowerASCII a no-op (`return b`) fails on every uppercase-hex
// entry, first with `hasDotSegment("/%2E") = false, reference = true` and then
// on `/%2E%2E`, `/.%2E`, `/%2E.` and the encoded separators. That is precisely
// the bypass the fold exists to prevent: url.URL.EscapedPath emits UPPERCASE
// hex, so a scan that does not fold sees none of the encodings it is looking
// for on any path the stdlib actually produces.
func Test_pathScansMatchTheirReferenceOnTheCorpus(t *testing.T) {
	t.Parallel()
	for _, path := range adversarialPaths {
		//: the separator scan must agree with the spelling it replaced.
		if got, want := hasEncodedSeparator(path), referenceHasEncodedSeparator(path); got != want {
			t.Errorf("hasEncodedSeparator(%q) = %v, reference = %v", path, got, want)
		}
		//: and so must the dot-segment scan.
		if got, want := hasDotSegment(path), referenceHasDotSegment(path); got != want {
			t.Errorf("hasDotSegment(%q) = %v, reference = %v", path, got, want)
		}
	}
}

// fuzzAlphabet is the byte vocabulary the random paths are drawn from. It is
// deliberately concentrated on the characters the scans care about, so a random
// path is far more likely to be interesting than a uniformly random string
// would be — and it includes the multi-byte runes whose folding is the only
// part of the equivalence that is not immediate.
var fuzzAlphabet = []string{
	"%", "2", "e", "E", "f", "F", "5", "c", "C", ".", "/", "a", "Z", "0",
	"İ", "K", "é", "\xff",
}

// Test_pathScansMatchTheirReferenceOnRandomPaths widens the corpus beyond what
// a person would think to enumerate.
//
// The seed is fixed so a failure is reproducible: a path check that disagrees
// with its reference on one input in a hundred thousand is a bypass, and
// "re-run it and see" is not a way to investigate one.
//
// MUTATION: relaxing the length guard in foldsASCII to `len(s) < len(want)` and
// comparing only len(want) bytes fails BOTH equivalence tests — this one on
// `hasDotSegment(".%") = true, reference = false`, and the corpus one on
// `hasDotSegment("/%2ea") = true, reference = false`. A dot-segment check that
// fires on a PREFIX refuses ordinary identifiers, so the mutation trades a
// bypass for an outage; the corpus alone would have caught it, and the random
// paths say how quickly.
func Test_pathScansMatchTheirReferenceOnRandomPaths(t *testing.T) {
	t.Parallel()
	const paths int = 200000
	const maxTokens int = 12
	source := rand.New(rand.NewPCG(0x5EED, 0xC0FFEE))
	var builder strings.Builder
	for range paths {
		builder.Reset()
		for range source.IntN(maxTokens) + 1 {
			builder.WriteString(fuzzAlphabet[source.IntN(len(fuzzAlphabet))])
		}
		path := builder.String()
		//: the separator scan must agree with the spelling it replaced.
		if got, want := hasEncodedSeparator(path), referenceHasEncodedSeparator(path); got != want {
			t.Fatalf("hasEncodedSeparator(%q) = %v, reference = %v", path, got, want)
		}
		//: and so must the dot-segment scan.
		if got, want := hasDotSegment(path), referenceHasDotSegment(path); got != want {
			t.Fatalf("hasDotSegment(%q) = %v, reference = %v", path, got, want)
		}
	}
}
