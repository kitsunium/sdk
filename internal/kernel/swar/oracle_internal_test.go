package swar

import (
	"math/rand/v2"
	"testing"
)

// The oracle is the definition of every search, written as the byte loop
// anyone would write: it shares no line with the lane tests it judges, and
// its membership predicate is stated again per spec, not read from a Set.

// spec is one search to hold to the oracle: the predicate that defines it,
// and every form of it that this build has — the public function, the SWAR
// form, and the vector form when the CPU runs one.
type spec struct {
	// name labels the spec in a failure.
	name string
	// member is the oracle's predicate: whether a byte is a match.
	member func(c byte) bool
	// public is the exported function (or method) for this search.
	public func(b []byte) int
	// swar is the SWAR form, called directly so that a CPU with AVX2 does not
	// hide it behind the vector form.
	swar func(b []byte) int
	// vector is the vector form, nil where this build or CPU has none or the
	// search has none (a bound of zero), for slices of vectorMin bytes or more.
	vector func(b []byte) int
}

// oracleIndex is the index of the first byte of b that member accepts, or -1.
func oracleIndex(b []byte, member func(byte) bool) int {
	for i, c := range b {
		if member(c) {
			return i
		}
	}
	return -1
}

// check holds every form of s to the oracle on b, and fails naming the form.
func (s spec) check(tb testing.TB, b []byte) {
	tb.Helper()
	want := oracleIndex(b, s.member)
	if got := s.public(b); got != want {
		tb.Fatalf("%s: public over %d bytes = %d, want %d\nb = % x", s.name, len(b), got, want, b)
	}
	if got := s.swar(b); got != want {
		tb.Fatalf("%s: SWAR over %d bytes = %d, want %d\nb = % x", s.name, len(b), got, want, b)
	}
	if s.vector != nil && len(b) >= vectorMin {
		if got := s.vector(b); got != want {
			tb.Fatalf("%s: vector over %d bytes = %d, want %d\nb = % x", s.name, len(b), got, want, b)
		}
	}
}

// byte2Spec is IndexByte2(b, c1, c2).
func byte2Spec(c1, c2 byte) spec {
	return spec{
		name:   "IndexByte2",
		member: func(x byte) bool { return x == c1 || x == c2 },
		public: func(b []byte) int { return IndexByte2(b, c1, c2) },
		swar:   func(b []byte) int { return swarIndexByte2(b, c1, c2) },
		vector: vectorByte2(c1, c2),
	}
}

// lessSpec is IndexLess(b, n).
func lessSpec(n byte) spec {
	return spec{
		name:   "IndexLess",
		member: func(x byte) bool { return x < n },
		public: func(b []byte) int { return IndexLess(b, n) },
		swar:   func(b []byte) int { return swarIndexLess(b, n) },
		vector: vectorLess(n),
	}
}

// setSpec is NewSet(first, second, below, upper).Index(b).
func setSpec(first, second, below byte, upper bool) spec {
	set := NewSet(first, second, below, upper)
	return spec{
		name: "Set.Index",
		member: func(x byte) bool {
			return x == first || x == second || x < below || (upper && x >= 0x80)
		},
		public: set.Index,
		swar:   func(b []byte) int { return swarIndexSet(b, set) },
		vector: vectorSet(set),
	}
}

// bytesUnderTest are the bytes the specs are built from: the line ends, the
// quote and backslash, the edges of the ASCII range and of a lane, and both
// neighbours of each — the values where an off-by-one in a lane test lives.
var bytesUnderTest = []byte{0x00, 0x01, 0x0A, 0x0D, 0x1F, 0x20, 0x21, 0x22, 0x5C, 0x7E, 0x7F, 0x80, 0x81, 0xFE, 0xFF}

// boundsUnderTest are the bounds IndexLess and Set are built with: zero, the
// edges of the exact range (127, 128) and of the byte loop (129), and the
// bounds a control-byte search uses.
var boundsUnderTest = []byte{0, 1, 0x20, 0x7F, 0x80, 0x81, 0xFF}

// specs returns every spec the suites run: each single byte (a pair of it
// twice), a spread of pairs, each bound, and sets of every shape (with and without each part).
func specs() []spec {
	var all []spec
	for _, c1 := range bytesUnderTest {
		// the pair with the next byte up and with the same byte twice: a search for one byte.
		all = append(all, byte2Spec(c1, c1+1), byte2Spec(c1, c1))
	}
	all = append(all, byte2Spec('\r', '\n'), byte2Spec(0x00, 0xFF))
	for _, n := range boundsUnderTest {
		all = append(all, lessSpec(n))
	}
	for _, below := range boundsUnderTest {
		for _, upper := range []bool{false, true} {
			all = append(all, setSpec('"', '\\', below, upper))
		}
	}
	// the escaping set, and the degenerate sets: one byte, none, and the zero Set.
	all = append(all,
		setSpec('"', '\\', 0x20, true),
		setSpec('x', 'x', 0, false),
		setSpec(0x00, 0x00, 0, false),
		setSpec(0x7F, 0x80, 0, false),
		setSpec(0xFF, 0xFE, 0x80, false),
	)
	return all
}

// randomBytes fills b with bytes drawn from a mixture that makes a match
// likely, and a match near the front unlikely: half the bytes are drawn from
// bytesUnderTest, a quarter from the whole range, the rest are 'a'.
func randomBytes(rng *rand.Rand, b []byte) {
	for i := range b {
		switch rng.IntN(4) {
		case 0, 1:
			b[i] = bytesUnderTest[rng.IntN(len(bytesUnderTest))]
		case 2:
			b[i] = byte(rng.IntN(256))
		default:
			b[i] = 'a'
		}
	}
}

// poisoned returns a slice of n bytes (filled by fill) whose capacity runs
// on past n, the bytes after it holding "\r\n\r\n" repeated: what a recycled
// receive buffer holds after the request before. A search that reads past
// the length finds a line end there.
func poisoned(n int, fill func(b []byte)) []byte {
	const extra = 64
	buf := make([]byte, n+extra)
	for i := n; i < len(buf); i++ {
		buf[i] = "\r\n\r\n"[(i-n)%4]
	}
	b := buf[: n : n+extra]
	fill(b)
	return b
}
