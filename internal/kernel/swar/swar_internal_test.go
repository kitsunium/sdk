package swar

import (
	"math/rand/v2"
	"testing"
)

// coverageValues is the thinned grid of byte values a coverage build runs the
// two exhaustive suites over: every edge a lane test has (0, 1, the control
// range's end, quote, backslash, DEL, both sides of 0x80, 0xFE, 0xFF) and
// every seventh value between. The full 256 take 108 s of the 60 s .bazelrc
// gives the target once the race detector and the coverage counters are both
// on (42 s and 66 s for the two suites, against 12 s without the counters), and
// the line coverage is the same 100% either way. The race suite, the alloc
// lane and a plain `go test` run all 256; this is the one place that does not.
var coverageValues = func() []int {
	edges := map[int]bool{
		0: true, 1: true, 0x0A: true, 0x0D: true, 0x1F: true, 0x20: true, 0x21: true,
		0x22: true, 0x5C: true, 0x7E: true, 0x7F: true, 0x80: true, 0x81: true, 0xFE: true, 0xFF: true,
	}
	var vs []int
	for v := range 256 {
		if edges[v] || v%7 == 0 {
			vs = append(vs, v)
		}
	}
	return vs
}()

// exhaustiveValues returns every byte value, or the thinned grid when the
// binary is a coverage build (see coverageValues).
func exhaustiveValues() []int {
	if testing.CoverMode() != "" {
		return coverageValues
	}
	all := make([]int, 256)
	for v := range all {
		all[v] = v
	}
	return all
}

// lengthsUnderTest are the slice lengths the lane suites run: none, the
// tail-only lengths, a word and its neighbours, a vector and its neighbours,
// and two words and two vectors past them — where the word loop, the tail
// and the overlapping last register each change.
var lengthsUnderTest = []int{0, 1, 2, 7, 8, 9, 15, 16, 17, 31, 32, 33, 39, 40, 63, 64, 65, 71}

// exhaustiveLengths are the lengths the two exhaustive suites run, a subset
// of lengthsUnderTest that keeps each edge: they cost a call per value per
// position.
var exhaustiveLengths = []int{1, 7, 8, 9, 15, 16, 17, 31, 32, 33, 40, 65}

// backgroundsUnderTest are the bytes that fill a slice around the one under
// test: a byte in no set but the widest ones ('a'), and the extreme of a lane
// that every bound a control-byte search uses lies below.
var backgroundsUnderTest = []byte{'a', 0x00}

// exhaustiveSpecs are the specs the two exhaustive suites run: one of every
// shape and edge — each part of a Set alone and together, the bounds either
// side of 128, a pair that straddles 0x80 — where the whole of specs() would
// multiply their cost by four for no lane the rest do not reach.
func exhaustiveSpecs() []spec {
	return []spec{
		byte2Spec(0x00, 0x00), byte2Spec(0x0A, 0x0A), byte2Spec(0x22, 0x22), byte2Spec(0x80, 0x80), byte2Spec(0xFF, 0xFF),
		byte2Spec('\r', '\n'), byte2Spec(0x7F, 0x80), byte2Spec(0x00, 0xFF),
		lessSpec(0), lessSpec(0x20), lessSpec(0x80), lessSpec(0x81),
		setSpec('"', '\\', 0x20, true), setSpec('x', 'x', 0, false),
		setSpec(0xFF, 0xFE, 0x80, false), setSpec('"', '\\', 0x81, true),
	}
}

// TestEveryValueAtEveryPosition puts each of the 256 byte values at each
// position of each length, in front of every background, and holds every
// form of every spec to the oracle: a lane test wrong for one value in one
// lane — the fault a random corpus meets once in a thousand runs — fails here
// every run.
func TestEveryValueAtEveryPosition(t *testing.T) {
	values := exhaustiveValues()
	for _, s := range exhaustiveSpecs() {
		for _, n := range exhaustiveLengths {
			b := make([]byte, n)
			for _, bg := range backgroundsUnderTest {
				for i := range b {
					b[i] = bg
				}
				for pos := range n {
					for _, v := range values {
						b[pos] = byte(v)
						s.check(t, b)
					}
					b[pos] = bg
				}
			}
		}
	}
}

// pairPositions are the positions a pair of adjacent bytes starts at: a lane
// edge, the last lane of a word and the first of the next, and the same at
// the edge of a vector — the places a carry out of one lane would reach the
// next.
var pairPositions = []int{0, 7, 30, 31}

// TestAdjacentPairs puts every ordered pair of byte values side by side, in a
// slice of 40 bytes, and holds every form to the oracle. A lane test that
// borrows from its neighbour — (v-0x01..)&^v&0x80.. flags the byte above a
// true match whatever it holds — is wrong for some pair and right for every
// single byte, which is why the suite above cannot catch it.
func TestAdjacentPairs(t *testing.T) {
	values := exhaustiveValues()
	for _, s := range exhaustiveSpecs() {
		b := make([]byte, 40)
		for i := range b {
			b[i] = 'a'
		}
		for _, pos := range pairPositions {
			for _, v1 := range values {
				for _, v2 := range values {
					b[pos], b[pos+1] = byte(v1), byte(v2)
					s.check(t, b)
				}
			}
			b[pos], b[pos+1] = 'a', 'a'
		}
	}
}

// TestFirstOfMany fills a slice with matches and checks the first one is
// returned, at every length: a form that returned the last, or the first of
// the last word, passes the suites with one match in them.
func TestFirstOfMany(t *testing.T) {
	for _, s := range specs() {
		for _, n := range lengthsUnderTest {
			b := make([]byte, n)
			for start := range n {
				for i := range b {
					b[i] = 'a'
				}
				// matches from start to the end: write a byte the spec accepts.
				for i := start; i < n; i++ {
					for v := range 256 {
						if s.member(byte(v)) {
							b[i] = byte(v)
							break
						}
					}
				}
				s.check(t, b)
			}
		}
	}
}

// TestRandomDifferential holds every form to the oracle over random slices of
// random lengths, drawn so that a match sits anywhere and often more than one
// does. The seed is fixed: a failure is the same failure next run.
func TestRandomDifferential(t *testing.T) {
	rng := rand.New(rand.NewPCG(0x5741, 0x52))
	all := specs()
	for range 40000 {
		s := all[rng.IntN(len(all))]
		b := make([]byte, rng.IntN(200))
		randomBytes(rng, b)
		s.check(t, b)
	}
}

// TestRandomParameters runs the same differential over searches built from
// random bytes, bounds and flags rather than the fixed ones.
func TestRandomParameters(t *testing.T) {
	rng := rand.New(rand.NewPCG(0x5741, 0x53))
	for range 40000 {
		var s spec
		c1, c2, n := byte(rng.IntN(256)), byte(rng.IntN(256)), byte(rng.IntN(256))
		switch rng.IntN(4) {
		case 0:
			s = byte2Spec(c1, c1)
		case 1:
			s = byte2Spec(c1, c2)
		case 2:
			s = lessSpec(n)
		default:
			s = setSpec(c1, c2, n, rng.IntN(2) == 0)
		}
		b := make([]byte, rng.IntN(150))
		randomBytes(rng, b)
		// the parameters themselves are in the slice often enough to be found.
		for range rng.IntN(3) {
			if len(b) > 0 {
				b[rng.IntN(len(b))] = []byte{c1, c2, n, n - 1}[rng.IntN(4)]
			}
		}
		s.check(t, b)
	}
}

// TestNothingPastTheLength gives every form a slice whose capacity runs on
// into "\r\n\r\n", the bytes a recycled receive buffer holds after the
// request before it, and requires the answer the length alone gives. A form
// that read a word, or a register, past len(b) would find a line end in the
// capacity, and answer an index beyond the slice.
func TestNothingPastTheLength(t *testing.T) {
	crlf := []spec{
		byte2Spec('\r', '\r'), byte2Spec('\n', '\n'), byte2Spec('\r', '\n'), lessSpec(0x20), lessSpec(0x0E),
		setSpec('\r', '\n', 0, false), setSpec('"', '\\', 0x20, true), setSpec(0x01, 0x02, 0x0E, false),
	}
	for _, s := range crlf {
		for n := range 100 {
			// 'a' fill: no spec above accepts it, so the only matches are the poison.
			b := poisoned(n, func(b []byte) {
				for i := range b {
					b[i] = 'a'
				}
			})
			if s.member('a') {
				t.Fatalf("%s accepts 'a': the poison would not be the only match", s.name)
			}
			s.check(t, b)
			if got := s.public(b); got != -1 {
				t.Fatalf("%s: found a match at %d in %d bytes of 'a', past which the capacity holds \\r\\n", s.name, got, n)
			}
		}
	}
}

// TestPoisonedCapacityRandom is the same property over random contents: the
// answer is the oracle's over the length, whatever the capacity holds.
func TestPoisonedCapacityRandom(t *testing.T) {
	rng := rand.New(rand.NewPCG(0x5741, 0x54))
	all := specs()
	for range 20000 {
		s := all[rng.IntN(len(all))]
		b := poisoned(rng.IntN(150), func(b []byte) { randomBytes(rng, b) })
		s.check(t, b)
	}
}

// TestEmptyAndNil pins the answers for no input at all.
func TestEmptyAndNil(t *testing.T) {
	for _, s := range specs() {
		if got := s.public(nil); got != -1 {
			t.Errorf("%s over nil = %d, want -1", s.name, got)
		}
		if got := s.public([]byte{}); got != -1 {
			t.Errorf("%s over an empty slice = %d, want -1", s.name, got)
		}
	}
}

// TestZeroSetHoldsNUL pins what the Set doc says: the zero value holds the
// byte 0x00 alone.
func TestZeroSetHoldsNUL(t *testing.T) {
	var s Set
	for _, n := range lengthsUnderTest {
		b := make([]byte, n)
		for i := range b {
			b[i] = 'a'
		}
		if got := s.Index(b); got != -1 {
			t.Fatalf("the zero Set found %d in %d bytes of 'a'", got, n)
		}
		if n > 0 {
			b[n-1] = 0
			if got := s.Index(b); got != n-1 {
				t.Fatalf("the zero Set over %d bytes ending in NUL = %d, want %d", n, got, n-1)
			}
		}
	}
}

// TestLaneTestsAreExact pins the property the package doc claims, on the lane
// functions themselves: a flag for a lane that matches and for no other, for
// every value of the lane and every value of its neighbour. The borrowing
// form flags the neighbour of a match, so this fails for it.
func TestLaneTestsAreExact(t *testing.T) {
	for lane := range 7 {
		for v := range 256 {
			for neighbour := range 256 {
				word := uint64(v)<<(8*lane) | uint64(neighbour)<<(8*(lane+1))
				want := func(match func(byte) bool) uint64 {
					var m uint64
					if match(byte(v)) {
						m |= 0x80 << (8 * lane)
					}
					if match(byte(neighbour)) {
						m |= 0x80 << (8 * (lane + 1))
					}
					// every other lane is zero: the oracle judges them as well.
					for other := range 8 {
						if other != lane && other != lane+1 && match(0) {
							m |= 0x80 << (8 * other)
						}
					}
					return m
				}
				if got, w := zeroLanes(word), want(func(c byte) bool { return c == 0 }); got != w {
					t.Fatalf("zeroLanes(%#016x) = %#016x, want %#016x", word, got, w)
				}
				for _, n := range []byte{0, 1, 0x20, 0x7F, 0x80} {
					if got, w := lessLanes(word, lessAdd(n)), want(func(c byte) bool { return c < n }); got != w {
						t.Fatalf("lessLanes(%#016x, n=%d) = %#016x, want %#016x", word, n, got, w)
					}
				}
			}
		}
	}
}

// FuzzSearch holds every form of the three searches to the oracle over bytes
// the fuzzer chooses, parameters the fuzzer chooses, and a capacity beyond
// the length filled with "\r\n\r\n". The seed corpus is every case the suites
// above found worth a name.
func FuzzSearch(f *testing.F) {
	f.Add([]byte("GET / HTTP/1.1\r\nHost: a\r\n\r\n"), byte('\r'), byte('\n'), byte(0x20), true)
	f.Add([]byte(`{"k":"a\"b\\c"}`), byte('"'), byte('\\'), byte(0x20), true)
	f.Add([]byte("caf\xc3\xa9\x1f"), byte(0xC3), byte(0xA9), byte(0x80), false)
	f.Add(make([]byte, 40), byte(0), byte(0xFF), byte(129), true)
	f.Add([]byte{}, byte(0), byte(0), byte(0), false)
	f.Fuzz(func(t *testing.T, data []byte, c1, c2, n byte, upper bool) {
		// the bytes are copied into a slice with poisoned capacity.
		b := poisoned(len(data), func(b []byte) { copy(b, data) })
		for _, s := range []spec{byte2Spec(c1, c1), byte2Spec(c1, c2), lessSpec(n), setSpec(c1, c2, n, upper)} {
			s.check(t, b)
		}
	})
}
