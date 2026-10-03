package msgpack_test

import (
	"bytes"
	"errors"
	"io"
	"math"
	"reflect"
	"testing"
	"testing/iotest"
	"time"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	coremsgpack "github.com/kitsunium/sdk/internal/core/data/codec/msgpack"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/msgpack"
)

// fuzzRecord is the typed target the fuzzer also decodes into, so mutated
// input reaches the struct, map, slice, array, time and method decoders and
// not only the untyped one.
type fuzzRecord struct {
	I  int8               `msgpack:"i"`
	U  uint32             `msgpack:"u"`
	F  float32            `msgpack:"f"`
	S  string             `msgpack:"s"`
	B  []byte             `msgpack:"b"`
	A  [3]int16           `msgpack:"a"`
	L  []fuzzRecord       `msgpack:"l"`
	M  map[string]float64 `msgpack:"m"`
	K  map[int]string     `msgpack:"k"`
	N  map[any]bool       `msgpack:"n"`
	P  *fuzzRecord        `msgpack:"p"`
	T  time.Time          `msgpack:"t"`
	X  any                `msgpack:"x"`
	E  error              `msgpack:"e"`
	TC textCapture        `msgpack:"tc"`
}

// fuzzSeeds is the corpus: the vendor's golden encodings, a typed record,
// and the degenerate shapes a hand-written test tends not to cover — written
// as raw bytes, because the mutator works on bytes and a corpus of encoder
// output only teaches it the happy path.
func fuzzSeeds(f *testing.F) [][]byte {
	f.Helper()
	seeds := [][]byte{
		mustMarshalF(f, fuzzRecord{I: -3, S: "s", A: [3]int16{1, 2, 3}, M: map[string]float64{"x": 1.5}, T: time.Unix(1, 2)}),
		mustMarshalF(f, benchRecordOf(2)),
		{},
		{0xc1},
		{0xdd, 0xff, 0xff, 0xff, 0xff},
		{0xdf, 0xff, 0xff, 0xff, 0xff},
		{0xdb, 0xff, 0xff, 0xff, 0xff},
		{0xc9, 0xff, 0xff, 0xff, 0xff, 0x01},
		{0xd7, 0xff, 0xee, 0x6b, 0x28, 0x00, 0x00, 0x00, 0x00, 0x00},
		{0xd4, 0x05, 0x00},
		{0x81, 0x91, 0x01, 0x01},
		{0x01, 0x02},
		append(bytes.Repeat([]byte{0x91}, 1001), 0xc0),
		append(bytes.Repeat([]byte{0x81, 0xa1, 0x78}, 400), 0xc0),
	}
	for _, g := range readGolden(f) {
		seeds = append(seeds, g.wire)
	}
	return seeds
}

// mustMarshalF encodes a seed or fails the fuzz setup.
func mustMarshalF(f *testing.F, v any) []byte {
	f.Helper()
	b, err := msgpack.New().Marshal(v)
	if err != nil {
		f.Fatalf("seed Marshal(%T): %v", v, err)
	}
	return b
}

// FuzzUnmarshal holds four properties over arbitrary input:
//
//  1. Unmarshal never panics, into any or into a typed record; a failure is
//     always UNMARSHAL_FAILED with the package's code.
//  2. What decodes into any re-encodes, and re-decodes to an equal value.
//  3. A streaming Decoder agrees with Unmarshal on a single document — read
//     in place from the read-ahead, and framed one byte per read: same
//     value, then io.EOF.
//  4. Nothing decoded aliases the input: overwriting it afterwards changes
//     no decoded string or byte slice.
func FuzzUnmarshal(f *testing.F) {
	for _, seed := range fuzzSeeds(f) {
		f.Add(seed)
	}
	sc := streaming(f)
	c := msgpack.New()
	f.Fuzz(func(t *testing.T, data []byte) {
		var rec fuzzRecord
		checkFailure(t, c.Unmarshal(data, &rec))
		var v any
		err := c.Unmarshal(bytes.Clone(data), &v)
		checkFailure(t, err)
		if err != nil {
			return
		}
		input := bytes.Clone(data)
		var fromInput any
		if derr := c.Unmarshal(input, &fromInput); derr != nil {
			t.Fatalf("second decode of the same bytes failed: %v", derr)
		}
		for i := range input {
			input[i] = 0xaa
		}
		if !equalAny(v, fromInput) {
			t.Fatalf("decoded value aliases its input:\n before %#v\n after  %#v", v, fromInput)
		}
		reencoded, merr := c.Marshal(v)
		if merr != nil {
			t.Fatalf("decoded value does not re-encode: %v", merr)
		}
		var back any
		if derr := c.Unmarshal(reencoded, &back); derr != nil || !equalAny(v, back) {
			t.Fatalf("round trip differs (err=%v):\n first  %#v\n second %#v", derr, v, back)
		}
		//: in place (the read-ahead holds the document) and framed (one byte
		//: per read forces the piecewise path): both must agree with Unmarshal.
		checkStream(t, sc.NewDecoder(bytes.NewReader(data)), v)
		checkStream(t, sc.NewDecoder(iotest.OneByteReader(bytes.NewReader(data))), v)
	})
}

// checkStream requires a stream of one document to yield want, then io.EOF.
func checkStream(t *testing.T, dec codec.Decoder, want any) {
	t.Helper()
	var streamed any
	if serr := dec.Decode(&streamed); serr != nil || !equalAny(want, streamed) {
		t.Fatalf("stream disagrees with Unmarshal (err=%v):\n stream %#v\n whole  %#v", serr, streamed, want)
	}
	if eof := dec.Decode(&streamed); !errors.Is(eof, io.EOF) {
		t.Fatalf("stream after the only value: %v, want io.EOF", eof)
	}
}

// checkFailure requires a decode failure to carry UNMARSHAL_FAILED and the
// package's code.
func checkFailure(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if code, ok := errs.CodeOf(err); !ok || code != coremsgpack.CodeMsgPackUnmarshalFailed || !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Fatalf("failure is not UNMARSHAL_FAILED %s: %v", coremsgpack.CodeMsgPackUnmarshalFailed, err)
	}
}

// equalAny compares untyped decodes: integers by value whatever their Go
// width (a positive int 16 re-encodes as the shorter uint 16 and comes back
// as uint16), NaN equals itself bit for bit, times compare as instants,
// everything else as reflect.DeepEqual.
func equalAny(a, b any) bool {
	if an, aok := integerOf(a); aok {
		bn, bok := integerOf(b)
		return bok && an == bn
	}
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && math.Float64bits(x) == math.Float64bits(y)
	case float32:
		y, ok := b.(float32)
		return ok && math.Float32bits(x) == math.Float32bits(y)
	case time.Time:
		y, ok := b.(time.Time)
		return ok && x.Equal(y)
	case []any:
		y, ok := b.([]any)
		return ok && equalSlices(x, y)
	case map[string]any:
		y, ok := b.(map[string]any)
		return ok && equalMaps(x, y)
	default:
		return reflect.DeepEqual(a, b)
	}
}

// equalSlices compares two untyped slices element by element.
func equalSlices(a, b []any) bool {
	if len(a) != len(b) || (a == nil) != (b == nil) {
		return false
	}
	for i := range a {
		if !equalAny(a[i], b[i]) {
			return false
		}
	}
	return true
}

// equalMaps compares two untyped maps key by key.
func equalMaps(a, b map[string]any) bool {
	if len(a) != len(b) || (a == nil) != (b == nil) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !equalAny(av, bv) {
			return false
		}
	}
	return true
}

// integerValue is an integer of any Go width, as a sign and a magnitude.
type integerValue struct {
	negative  bool
	magnitude uint64
}

// integerOf normalises the integer types an untyped decode produces.
func integerOf(v any) (integerValue, bool) {
	switch x := v.(type) {
	case int8:
		return signedValue(int64(x)), true
	case int16:
		return signedValue(int64(x)), true
	case int32:
		return signedValue(int64(x)), true
	case int64:
		return signedValue(x), true
	case uint8:
		return integerValue{magnitude: uint64(x)}, true
	case uint16:
		return integerValue{magnitude: uint64(x)}, true
	case uint32:
		return integerValue{magnitude: uint64(x)}, true
	case uint64:
		return integerValue{magnitude: x}, true
	default:
		return integerValue{}, false
	}
}

// signedValue splits n into a sign and a magnitude.
func signedValue(n int64) integerValue {
	if n < 0 {
		return integerValue{negative: true, magnitude: uint64(-(n + 1)) + 1}
	}
	return integerValue{magnitude: uint64(n)}
}
