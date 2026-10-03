package cbor

import (
	"bytes"
	"encoding/hex"
	"math"
	"testing"
)

// Test_appendHead pins the shortest head for every boundary of the five head
// widths, in every major type that carries an argument.
func Test_appendHead(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		major majorType
		arg   uint64
		want  string
	}
	tests := []tc{
		{"0 direct", majorUnsigned, 0, "00"},
		{"23 direct", majorUnsigned, 23, "17"},
		{"24 one byte", majorUnsigned, 24, "1818"},
		{"255 one byte", majorUnsigned, 255, "18ff"},
		{"256 two bytes", majorUnsigned, 256, "190100"},
		{"65535 two bytes", majorUnsigned, 65535, "19ffff"},
		{"65536 four bytes", majorUnsigned, 65536, "1a00010000"},
		{"2^32-1 four bytes", majorUnsigned, math.MaxUint32, "1affffffff"},
		{"2^32 eight bytes", majorUnsigned, math.MaxUint32 + 1, "1b0000000100000000"},
		{"2^64-1 eight bytes", majorUnsigned, math.MaxUint64, "1bffffffffffffffff"},
		{"negative", majorNegative, 0, "20"},
		{"byte string length", majorBytes, 24, "5818"},
		{"text length", majorText, 5, "65"},
		{"array count", majorArray, 1000, "9903e8"},
		{"map count", majorMap, 1, "a1"},
		{"tag number", majorTag, 55799, "d9d9f7"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := hex.EncodeToString(appendHead(nil, tc.major, tc.arg))
		if got != tc.want {
			t.Errorf("%s: appendHead = %s, want %s", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_readHead decodes every head width and reports a cut-short head as
// incomplete rather than reading past the input.
func Test_readHead(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		data     string
		wantOK   bool
		wantArg  uint64
		wantSize int
		wantInfo byte
	}
	tests := []tc{
		{"direct", "17", true, 23, 1, 23},
		{"one byte", "18ff", true, 255, 2, info1Byte},
		{"two bytes", "190100", true, 256, 3, info2Bytes},
		{"four bytes", "1a00010000", true, 65536, 5, info4Bytes},
		{"eight bytes", "1bffffffffffffffff", true, math.MaxUint64, 9, info8Bytes},
		{"reserved 28 carries no argument", "1c", true, 0, 1, 28},
		{"indefinite carries no argument", "9f", true, 0, 1, infoIndefinite},
		{"empty input", "", false, 0, 0, 0},
		{"one byte argument missing", "18", false, 0, 0, 0},
		{"eight byte argument cut", "1b00000000", false, 0, 0, 0},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		data, err := hex.DecodeString(tc.data)
		if err != nil {
			t.Fatalf("%s: bad hex fixture: %v", tc.name, err)
		}
		h, ok := readHead(data, 0)
		if ok != tc.wantOK {
			t.Fatalf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
		}
		if ok && (h.arg != tc.wantArg || h.size != tc.wantSize || h.info != tc.wantInfo) {
			t.Errorf("%s: head = %+v, want arg %d size %d info %d", tc.name, h, tc.wantArg, tc.wantSize, tc.wantInfo)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_argumentWidth covers the four argument widths and the values with none.
func Test_argumentWidth(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		info byte
		want int
	}
	tests := []tc{
		{"direct", 23, 0},
		{"one", info1Byte, 1},
		{"two", info2Bytes, 2},
		{"four", info4Bytes, 4},
		{"eight", info8Bytes, 8},
		{"reserved", infoReservedMin, 0},
		{"indefinite", infoIndefinite, 0},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if got := argumentWidth(tc.info); got != tc.want {
			t.Errorf("%s: argumentWidth(%d) = %d, want %d", tc.name, tc.info, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_readUint reads each width big-endian.
func Test_readUint(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		info byte
		want uint64
	}
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	tests := []tc{
		{"one", info1Byte, 0x01},
		{"two", info2Bytes, 0x0102},
		{"four", info4Bytes, 0x01020304},
		{"eight", info8Bytes, 0x0102030405060708},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if got := readUint(data, tc.info); got != tc.want {
			t.Errorf("%s: readUint = %#x, want %#x", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// referenceHalf decodes a binary16 by its definition, independently of
// halfToFloat64: (−1)^s × 2^(e−15) × (1 + m/1024), subnormals 2^−14 × m/1024.
func referenceHalf(half uint16) float64 {
	sign := 1.0
	if half&0x8000 != 0 {
		sign = -1
	}
	exponent := int(half>>10) & 0x1f
	mantissa := float64(half & 0x3ff)
	switch exponent {
	case 0:
		return sign * math.Ldexp(mantissa/1024, -14)
	case 0x1f:
		if mantissa == 0 {
			return math.Inf(int(sign))
		}
		return math.NaN()
	default:
		return sign * math.Ldexp(1+mantissa/1024, exponent-15)
	}
}

// Test_halfToFloat64 checks every one of the 65 536 binary16 values against
// the reference decoding, signed zeros by their sign bit and NaNs by their
// payload.
func Test_halfToFloat64(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		from uint32
		to   uint32
	}
	tests := []tc{
		{"positive", 0, 0x8000},
		{"negative", 0x8000, 0x10000},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		for bits := tc.from; bits < tc.to; bits++ {
			half := uint16(bits)
			got, want := halfToFloat64(half), referenceHalf(half)
			if math.IsNaN(want) {
				payload := math.Float64bits(got) >> 42 & 0x3ff
				if !math.IsNaN(got) || uint16(payload) != half&0x3ff {
					t.Fatalf("%s: half %#04x: got %v, want NaN with its payload", tc.name, half, got)
				}
				continue
			}
			if got != want || math.Signbit(got) != math.Signbit(want) {
				t.Fatalf("%s: half %#04x: got %v, want %v", tc.name, half, got, want)
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_appendHead_roundTrip checks that every head readHead decodes is the
// head appendHead writes for the same argument.
func Test_appendHead_roundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		arg  uint64
	}
	tests := []tc{{"zero", 0}, {"byte", 200}, {"short", 60000}, {"word", 1 << 31}, {"long", 1 << 60}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		encoded := appendHead(nil, majorArray, tc.arg)
		h, ok := readHead(encoded, 0)
		if !ok || h.arg != tc.arg || h.size != len(encoded) || h.major != majorArray {
			t.Errorf("%s: round trip of %d gave %+v (ok %v) from %x", tc.name, tc.arg, h, ok, encoded)
		}
		if !bytes.Equal(appendHead(nil, h.major, h.arg), encoded) {
			t.Errorf("%s: re-encoding differs", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
