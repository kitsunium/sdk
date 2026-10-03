package cbor_test

import (
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/cbor"
)

// bigFromString parses a decimal big.Int fixture.
func bigFromString(t *testing.T, s string) big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad big.Int fixture %q", s)
	}
	return *n
}

// fivePairs is RFC 8949 Appendix A's {"a": "A", "b": "B", "c": "C", "d": "D",
// "e": "E"}: each letter keys its upper case.
func fivePairs() map[string]any {
	pairs := map[string]any{}
	for letter := range strings.SplitSeq("abcde", "") {
		pairs[letter] = strings.ToUpper(letter)
	}
	return pairs
}

// sameValue compares untyped results: NaN equals NaN, signed zeros by sign,
// times by instant, big.Ints by value.
func sameValue(a, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && (math.IsNaN(x) && math.IsNaN(y) || x == y && math.Signbit(x) == math.Signbit(y))
	case time.Time:
		y, ok := b.(time.Time)
		return ok && x.Equal(y)
	case big.Int:
		y, ok := b.(big.Int)
		return ok && x.Cmp(&y) == 0
	default:
		return reflect.DeepEqual(a, b)
	}
}

// TestUnmarshal_appendixA decodes the examples of RFC 8949 Appendix A into
// an untyped target.
func TestUnmarshal_appendixA(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		data string
		want any
	}
	seq := make([]any, 25)
	for i := range seq {
		seq[i] = uint64(i + 1)
	}
	tests := []tc{
		{"0", "00", uint64(0)},
		{"23", "17", uint64(23)},
		{"24", "1818", uint64(24)},
		{"1000000", "1a000f4240", uint64(1000000)},
		{"1000000000000", "1b000000e8d4a51000", uint64(1000000000000)},
		{"max uint64", "1bffffffffffffffff", maxUint64},
		{"2^64 bignum", "c249010000000000000000", bigFromString(t, "18446744073709551616")},
		{"-2^64", "3bffffffffffffffff", bigFromString(t, "-18446744073709551616")},
		{"-2^64-1 bignum", "c349010000000000000000", bigFromString(t, "-18446744073709551617")},
		{"-1", "20", int64(-1)},
		{"-1000", "3903e7", int64(-1000)},
		{"0.0", "f90000", 0.0},
		{"-0.0", "f98000", math.Copysign(0, -1)},
		{"1.0", "f93c00", 1.0},
		{"1.1", "fb3ff199999999999a", 1.1},
		{"65504.0", "f97bff", 65504.0},
		{"100000.0", "fa47c35000", 100000.0},
		{"max float32", "fa7f7fffff", 3.4028234663852886e+38},
		{"1e300", "fb7e37e43c8800759c", 1.0e+300},
		{"smallest subnormal half", "f90001", 5.960464477539063e-8},
		{"smallest normal half", "f90400", 0.00006103515625},
		{"-4.0", "f9c400", -4.0},
		{"half +Inf", "f97c00", math.Inf(1)},
		{"half NaN", "f97e00", math.NaN()},
		{"single -Inf", "faff800000", math.Inf(-1)},
		{"double NaN", "fb7ff8000000000000", math.NaN()},
		{"false", "f4", false},
		{"true", "f5", true},
		{"null", "f6", nil},
		{"undefined", "f7", nil},
		{"tag 0", "c074323031332d30332d32315432303a30343a30305a", time.Date(2013, 3, 21, 20, 4, 0, 0, time.UTC)},
		{"tag 1 integer", "c11a514b67b0", time.Unix(1363896240, 0)},
		{"tag 1 float", "c1fb41d452d9ec200000", time.Unix(1363896240, 500000000)},
		{"tag 23 is transparent", "d74401020304", []byte{1, 2, 3, 4}},
		{"tag 24 is transparent", "d818456449455446", []byte("dIETF")},
		{"tag 32 is transparent", "d82076687474703a2f2f7777772e6578616d706c652e636f6d", "http://www.example.com"},
		{"self-describe tag", "d9d9f71863", uint64(99)},
		{"empty bytes", "40", []byte{}},
		{"bytes", "4401020304", []byte{1, 2, 3, 4}},
		{"empty text", "60", ""},
		{"IETF", "6449455446", "IETF"},
		{"escapes", "62225c", "\"\\"},
		{"u umlaut", "62c3bc", "ü"},
		{"water", "63e6b0b4", "水"},
		{"supplementary plane", "64f0908591", "𐅑"},
		{"empty array", "80", []any{}},
		{"nested arrays", "8301820203820405", []any{uint64(1), []any{uint64(2), uint64(3)}, []any{uint64(4), uint64(5)}}},
		{"25 elements", "98190102030405060708090a0b0c0d0e0f101112131415161718181819", seq},
		{"empty map", "a0", map[string]any{}},
		{"integer keys", "a201020304", map[any]any{uint64(1): uint64(2), uint64(3): uint64(4)}},
		{"text keys", "a26161016162820203", map[string]any{"a": uint64(1), "b": []any{uint64(2), uint64(3)}}},
		{"map in array", "826161a161626163", []any{"a", map[string]any{"b": "c"}}},
		{"five pairs", "a56161614161626142616361436164614461656145", fivePairs()},
		{"indefinite bytes", "5f42010243030405ff", []byte{1, 2, 3, 4, 5}},
		{"indefinite text", "7f657374726561646d696e67ff", "streaming"},
		{"empty indefinite array", "9fff", []any{}},
		{"indefinite outer and inner", "9f018202039f0405ffff", []any{uint64(1), []any{uint64(2), uint64(3)}, []any{uint64(4), uint64(5)}}},
		{"indefinite inner", "83019f0203ff820405", []any{uint64(1), []any{uint64(2), uint64(3)}, []any{uint64(4), uint64(5)}}},
		{"indefinite 25", "9f0102030405060708090a0b0c0d0e0f101112131415161718181819ff", seq},
		{"indefinite map", "bf61610161629f0203ffff", map[string]any{"a": uint64(1), "b": []any{uint64(2), uint64(3)}}},
		{"indefinite map in array", "826161bf61626163ff", []any{"a", map[string]any{"b": "c"}}},
		{"Fun and Amt", "bf6346756ef563416d7421ff", map[string]any{"Fun": true, "Amt": int64(-2)}},
		{"text then integer keys", "a26161010203", map[any]any{"a": uint64(1), uint64(2): uint64(3)}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var got any
		if err := cbor.New().Unmarshal(mustHex(t, tc.data), &got); err != nil {
			t.Fatalf("%s: Unmarshal err = %v (%s)", tc.name, err, errs.PrivateOf(err))
		}
		if !sameDeep(got, tc.want) {
			t.Errorf("%s: got %#v (%T), want %#v (%T)", tc.name, got, got, tc.want, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// sameDeep compares two untyped trees with sameValue at the leaves.
func sameDeep(a, b any) bool {
	switch x := a.(type) {
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameDeep(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if w, found := y[k]; !found || !sameDeep(v, w) {
				return false
			}
		}
		return true
	default:
		return sameValue(a, b)
	}
}

// TestUnmarshal_untypedRefusals pins what an untyped target refuses: an
// unassigned simple value, and a map key Go cannot hash.
func TestUnmarshal_untypedRefusals(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		data   string
		detail string
	}
	tests := []tc{
		{"simple 16", "f0", "unassigned simple value"},
		{"simple 255", "f8ff", "unassigned simple value"},
		{"simple in an array", "8201f3", "unassigned simple value"},
		{"byte string key", "a1416101", "cannot be a Go map key"},
		{"array key", "a1810101", "cannot be a Go map key"},
		{"bignum key", "a1c249010000000000000000f5", "cannot be a Go map key"},
		{"tag 0 that is not a date", "c06161", "not an RFC 3339 date/time"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var got any
		err := cbor.New().Unmarshal(mustHex(t, tc.data), &got)
		if !errs.HasReason(err, "UNMARSHAL_FAILED") || !strings.Contains(errs.PrivateOf(err), tc.detail) {
			t.Errorf("%s: err = %v (%s), want %q", tc.name, err, errs.PrivateOf(err), tc.detail)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestUnmarshal_documentRefusals pins the refusals of the document itself:
// an empty input, trailing bytes, a malformed item, and a target that is not
// a non-nil pointer.
func TestUnmarshal_documentRefusals(t *testing.T) {
	t.Parallel()
	var n int
	type tc struct {
		name   string
		data   string
		target any
		detail string
	}
	tests := []tc{
		{"empty input", "", &n, "the input is empty"},
		{"trailing bytes", "0102", &n, "bytes follow the data item"},
		{"truncated", "19", &n, "ends inside a data item"},
		{"nil target", "01", nil, "non-nil pointer, got nil"},
		{"non-pointer target", "01", n, "non-nil pointer, got int"},
		{"nil pointer target", "01", (*int)(nil), "non-nil pointer, got *int"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := cbor.New().Unmarshal(mustHex(t, tc.data), tc.target)
		if !errs.HasReason(err, "UNMARSHAL_FAILED") || !strings.Contains(errs.PrivateOf(err), tc.detail) {
			t.Errorf("%s: err = %v (%s), want %q", tc.name, err, errs.PrivateOf(err), tc.detail)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestUnmarshal_malformedLeavesTargetUntouched shows the validation pass at
// work: a document refused for its form never writes into the target.
func TestUnmarshal_malformedLeavesTargetUntouched(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		data string
	}
	tests := []tc{
		{"truncated after the first field", "a2616101616202"[:12]},
		{"invalid UTF-8 in the last field", "a2616101616261ff"},
		{"trailing byte", "a161610100"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		target := map[string]int{"keep": 7}
		if err := cbor.New().Unmarshal(mustHex(t, tc.data), &target); err == nil {
			t.Fatalf("%s: accepted", tc.name)
		}
		if !reflect.DeepEqual(target, map[string]int{"keep": 7}) {
			t.Errorf("%s: target modified: %v", tc.name, target)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
