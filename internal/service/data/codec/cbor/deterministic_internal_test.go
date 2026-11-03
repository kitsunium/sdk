package cbor

import (
	"encoding/binary"
	"math"
	"testing"
)

// Test_appendShortestFloat_everyHalf checks the narrowing against every
// half-precision value: each one that is not NaN is written as itself, in
// two bytes — normals, subnormals, both zeros and both infinities.
func Test_appendShortestFloat_everyHalf(t *testing.T) {
	t.Parallel()
	for bits := range uint32(math.MaxUint16 + 1) {
		half := uint16(bits)
		f := halfToFloat64(half)
		//: every NaN is written 0xf97e00, which the vectors cover.
		if math.IsNaN(f) {
			continue
		}
		got := appendShortestFloat(nil, f)
		if len(got) != 3 || got[0] != float16Head || binary.BigEndian.Uint16(got[1:]) != half {
			t.Fatalf("half %#04x (%v) written %x", half, f, got)
		}
	}
}

// Test_appendShortestFloat_widths checks the single and double rows: a value
// one bit finer than a half is a single, one bit finer than a single a double.
func Test_appendShortestFloat_widths(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		f    float64
		size int
	}{
		{1 + 0x1p-10, 3},
		{1 + 0x1p-11, 5},
		{1 + 0x1p-23, 5},
		{1 + 0x1p-24, 9},
		{0x1p-24, 3},
		{0x1p-25, 5},
		{0x1.8p-24, 5},
		{65504, 3},
		{65505, 5},
		{65536, 5},
		{math.MaxFloat32, 5},
		{math.MaxFloat64, 9},
		{math.SmallestNonzeroFloat32, 5},
		{math.SmallestNonzeroFloat64, 9},
	} {
		got := appendShortestFloat(nil, tc.f)
		if len(got) != tc.size {
			t.Fatalf("%v written in %d bytes (%x), want %d", tc.f, len(got), got, tc.size)
		}
		if back := floatOf(headOf(t, got)); back != tc.f {
			t.Fatalf("%v read back as %v", tc.f, back)
		}
	}
}

// headOf reads the float head written at the start of b.
func headOf(t *testing.T, b []byte) itemHead {
	t.Helper()
	h, ok := readHead(b, 0)
	if !ok {
		t.Fatalf("no head in %x", b)
	}
	return h
}
