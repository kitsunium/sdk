package frozen

import "math/bits"

// The odd multipliers the hashes and the slot function fold with. Any odd
// 64-bit constant with its bits spread works; these are the golden ratio's
// and two of its neighbours, fixed forever because an image stores hashes'
// consequences (its pilots) and is read by a later build.
const (
	mulA uint64 = 0x9E3779B97F4A7C15
	mulB uint64 = 0xD6E8FEB86659FD93
	mulC uint64 = 0xC2B2AE3D27D4EB4F
)

// fold multiplies a by b into 128 bits and folds the halves together: every
// bit of either input reaches the middle of the result, which is the whole
// mixing step of both hashes.
func fold(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	return hi ^ lo
}

// below maps x onto [0, n) by the high half of x*n — a range reduction with no
// division, uniform when x is.
func below(x uint64, n uint32) uint32 {
	hi, _ := bits.Mul64(x, uint64(n))
	return uint32(hi)
}

// hashInt hashes an integer key, read as the uint64 its type converts to, under
// seed: the seed XORed in, then Stafford's thirteenth 64-bit finalizer (the
// one SplitMix64 ends with). It is a bijection, so two integer keys never share
// a hash. One folded multiply was measured too weak for it: on the keys 0 to
// 200 000 its hashes kept the inputs' structure and a build took two minutes
// instead of 40 ms.
func hashInt(seed, k uint64) uint64 {
	k ^= seed
	k = (k ^ k>>30) * 0xBF58476D1CE4E5B9
	k = (k ^ k>>27) * 0x94D049BB133111EB
	return k ^ k>>31
}

// load64 reads s[i:i+8] little-endian; the compiler merges the byte loads.
func load64(s string, i int) uint64 {
	_ = s[i+7]
	return uint64(s[i]) | uint64(s[i+1])<<8 | uint64(s[i+2])<<16 | uint64(s[i+3])<<24 |
		uint64(s[i+4])<<32 | uint64(s[i+5])<<40 | uint64(s[i+6])<<48 | uint64(s[i+7])<<56
}

// load32 reads s[i:i+4] little-endian.
func load32(s string, i int) uint64 {
	_ = s[i+3]
	return uint64(s[i]) | uint64(s[i+1])<<8 | uint64(s[i+2])<<16 | uint64(s[i+3])<<24
}

// hashString hashes a string key under seed: sixteen bytes per folded
// multiply, the tail read as two overlapping words so no byte is read twice
// into the same lane and none is skipped.
func hashString(seed uint64, s string) uint64 {
	h := seed ^ uint64(len(s))*mulA
	//: whole 16-byte blocks, each folded into the running state.
	for len(s) > 16 {
		h = fold(load64(s, 0)^h^mulB, load64(s, 8)^mulC)
		s = s[16:]
	}
	var a, b uint64
	switch n := len(s); {
	case n >= 8:
		a, b = load64(s, 0), load64(s, n-8)
	case n >= 4:
		a, b = load32(s, 0), load32(s, n-4)
	case n > 0:
		a = uint64(s[0])<<16 | uint64(s[n>>1])<<8 | uint64(s[n-1])
	}
	return fold(a^h^mulB, b^mulC^h)
}

// slotOf is where a key whose hash is h lands under pilot p, in a table of m
// slots: the pilot's multiple is XORed in and the product's high half reduced.
func slotOf(h uint64, p byte, m uint32) uint32 {
	return below((h^(uint64(p)+1)*mulC)*mulA, m)
}
