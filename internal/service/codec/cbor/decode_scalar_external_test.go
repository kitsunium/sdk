package cbor_test

import (
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/cbor"
)

// The integer extremes as typed constants, so that a table of any holds each
// at its own type rather than at int's.
const (
	// maxInt64 is the largest int64.
	maxInt64 int64 = math.MaxInt64
	// minInt64 is the smallest int64.
	minInt64 int64 = math.MinInt64
	// maxUint32 is the largest uint32, widened.
	maxUint32 uint64 = math.MaxUint32
	// maxUint64 is the largest uint64.
	maxUint64 uint64 = math.MaxUint64
)

// typedCase decodes data into a fresh target and compares the result.
type typedCase struct {
	// name labels the subtest.
	name string
	// data is the input, hex.
	data string
	// target returns a pointer to the value to decode into.
	target func() any
	// want is the expected pointed-to value; nil when only the error matters.
	want any
	// wantErr is a fragment of the expected Private message; "" for success.
	wantErr string
}

// runTyped runs a table of typed decoding cases.
func runTyped(t *testing.T, tests []typedCase) {
	t.Helper()
	runCase := func(t *testing.T, tc typedCase) {
		t.Helper()
		target := tc.target()
		err := cbor.New().Unmarshal(mustHex(t, tc.data), target)
		if tc.wantErr != "" {
			if !errs.HasReason(err, "UNMARSHAL_FAILED") || !strings.Contains(errs.PrivateOf(err), tc.wantErr) {
				t.Fatalf("%s: err = %v (%s), want %q", tc.name, err, errs.PrivateOf(err), tc.wantErr)
			}
		} else if err != nil {
			t.Fatalf("%s: err = %v (%s)", tc.name, err, errs.PrivateOf(err))
		}
		if tc.want == nil {
			return
		}
		got := reflect.ValueOf(target).Elem().Interface()
		if !equalTyped(got, tc.want) {
			t.Errorf("%s: got %#v, want %#v", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// equalTyped compares decoded values, times by instant and big.Ints by value.
func equalTyped(got, want any) bool {
	switch w := want.(type) {
	case time.Time:
		g, ok := got.(time.Time)
		return ok && g.Equal(w)
	case big.Int:
		g, ok := got.(big.Int)
		return ok && g.Cmp(&w) == 0
	default:
		return reflect.DeepEqual(got, want)
	}
}

// ptrTo returns a target factory for a fresh zero T.
func ptrTo[T any]() func() any {
	return func() any { return new(T) }
}

// ptrWith returns a target factory for a T holding initial.
func ptrWith[T any](initial T) func() any {
	return func() any {
		p := new(T)
		*p = initial
		return p
	}
}

// TestUnmarshal_integers pins integer decoding: stored where it fits, refused
// where it would overflow or change sign, bignums counting as integers.
func TestUnmarshal_integers(t *testing.T) {
	t.Parallel()
	runTyped(t, []typedCase{
		{"int8 max", "187f", ptrTo[int8](), int8(127), ""},
		{"int8 overflow", "19012c", ptrTo[int8](), int8(0), "into a Go value of type int8"},
		{"int8 min", "387f", ptrTo[int8](), int8(-128), ""},
		{"int8 underflow", "3880", ptrTo[int8](), nil, "of type int8"},
		{"int64 max", "1b7fffffffffffffff", ptrTo[int64](), maxInt64, ""},
		{"int64 overflow", "1b8000000000000000", ptrTo[int64](), nil, "of type int64"},
		{"int64 min", "3b7fffffffffffffff", ptrTo[int64](), minInt64, ""},
		{"int64 underflow", "3b8000000000000000", ptrTo[int64](), nil, "of type int64"},
		{"uint8 max", "18ff", ptrTo[uint8](), uint8(255), ""},
		{"uint8 overflow", "190100", ptrTo[uint8](), nil, "of type uint8"},
		{"uint negative", "20", ptrTo[uint](), nil, "a negative integer into"},
		{"uint64 max", "1bffffffffffffffff", ptrTo[uint64](), maxUint64, ""},
		{"bignum into int", "c24101", ptrTo[int](), 1, ""},
		{"negative bignum into int", "c34101", ptrTo[int](), -2, ""},
		{"bignum past int64", "c249010000000000000000", ptrTo[int64](), nil, "of type int64"},
		{"bignum into uint64", "c248ffffffffffffffff", ptrTo[uint64](), maxUint64, ""},
		{"negative bignum into uint", "c34101", ptrTo[uint](), nil, "of type uint"},
		{"float into int", "f93e00", ptrTo[int](), nil, "a floating-point number into"},
		{"text into int", "6161", ptrTo[int](), nil, "a text string into"},
		{"simple value into int", "f0", ptrTo[int](), nil, "an unassigned simple value into"},
		{"null leaves an int", "f6", ptrWith(7), 7, ""},
		{"tag 1 is transparent for an int", "c11a514b67b0", ptrTo[int64](), int64(1363896240), ""},
		{"unknown tag is transparent", "d8641863", ptrTo[int](), 99, ""},
	})
}

// TestUnmarshal_floats pins float decoding: every width, integers converted,
// a float32 refusing what it cannot hold.
func TestUnmarshal_floats(t *testing.T) {
	t.Parallel()
	runTyped(t, []typedCase{
		{"half into float32", "f93e00", ptrTo[float32](), float32(1.5), ""},
		{"double into float32", "fb3ff8000000000000", ptrTo[float32](), float32(1.5), ""},
		{"double past float32", "fb7e37e43c8800759c", ptrTo[float32](), nil, "of type float32"},
		{"infinity into float32", "f97c00", ptrTo[float32](), float32(math.Inf(1)), ""},
		{"unsigned into float64", "1864", ptrTo[float64](), 100.0, ""},
		{"negative into float64", "3863", ptrTo[float64](), -100.0, ""},
		{"negative past int64 into float64", "3b8000000000000000", ptrTo[float64](), nil, "of type float64"},
		{"bignum into float64", "c24101", ptrTo[float64](), 1.0, ""},
		{"bool into float64", "f5", ptrTo[float64](), nil, "a boolean into"},
	})
}

// TestUnmarshal_textAndBool pins strings and booleans.
func TestUnmarshal_textAndBool(t *testing.T) {
	t.Parallel()
	runTyped(t, []typedCase{
		{"text", "6449455446", ptrTo[string](), "IETF", ""},
		{"indefinite text", "7f657374726561646d696e67ff", ptrTo[string](), "streaming", ""},
		{"bytes into string", "4161", ptrTo[string](), nil, "a byte string into"},
		{"null leaves a string", "f6", ptrWith("keep"), "keep", ""},
		{"true", "f5", ptrTo[bool](), true, ""},
		{"false", "f4", ptrWith(true), false, ""},
		{"simple value into bool", "f0", ptrTo[bool](), nil, "an unassigned simple value into"},
		{"integer into bool", "01", ptrTo[bool](), nil, "an unsigned integer into"},
	})
}

// TestUnmarshal_time pins time.Time decoding: RFC 3339 text, Unix seconds as
// an integer or a float, under tag 0, 1, any other tag, or none.
func TestUnmarshal_time(t *testing.T) {
	t.Parallel()
	offset := time.FixedZone("", 2*60*60)
	runTyped(t, []typedCase{
		{"integer seconds", "1a6553f100", ptrTo[time.Time](), time.Unix(1700000000, 0), ""},
		{"negative seconds", "20", ptrTo[time.Time](), time.Unix(-1, 0), ""},
		{"float seconds", "fb41d952d9ec200000", ptrTo[time.Time](), time.Unix(1699440560, 500000000), ""},
		{"NaN is the zero time", "f97e00", ptrWith(time.Unix(5, 0)), time.Time{}, ""},
		{"RFC 3339", "74323031332d30332d32315432303a30343a30305a", ptrTo[time.Time](), time.Date(2013, 3, 21, 20, 4, 0, 0, time.UTC), ""},
		{
			"RFC 3339 with an offset", "7819323031332d30332d32315432303a30343a30302b30323a3030", ptrTo[time.Time](),
			time.Date(2013, 3, 21, 20, 4, 0, 0, offset), "",
		},
		{"tag 1", "c11a514b67b0", ptrTo[time.Time](), time.Unix(1363896240, 0), ""},
		{"another tag", "d8641a6553f100", ptrTo[time.Time](), time.Unix(1700000000, 0), ""},
		{"null leaves the time", "f6", ptrWith(time.Unix(5, 0)), time.Unix(5, 0), ""},
		{"bytes refused", "4161", ptrTo[time.Time](), nil, "a byte string into"},
		{"text that is not a date", "63616263", ptrTo[time.Time](), nil, "not an RFC 3339 date/time"},
		{"seconds past int64", "1b8000000000000000", ptrTo[time.Time](), nil, "of type time.Time"},
		{"float seconds past int64", "fb43e0000000000000", ptrTo[time.Time](), nil, "of type time.Time"},
	})
}

// TestUnmarshal_bigInt pins big.Int decoding.
func TestUnmarshal_bigInt(t *testing.T) {
	t.Parallel()
	runTyped(t, []typedCase{
		{"unsigned", "1864", ptrTo[big.Int](), *big.NewInt(100), ""},
		{"negative past int64", "3bffffffffffffffff", ptrTo[big.Int](), bigFromString(t, "-18446744073709551616"), ""},
		{"positive bignum", "c249010000000000000000", ptrTo[big.Int](), bigFromString(t, "18446744073709551616"), ""},
		{"negative bignum", "c349010000000000000000", ptrTo[big.Int](), bigFromString(t, "-18446744073709551617"), ""},
		{"through a pointer", "1864", ptrTo[*big.Int](), nil, ""},
		{"float refused", "f93e00", ptrTo[big.Int](), nil, "a floating-point number into"},
		{"another tag is transparent", "d8641864", ptrTo[big.Int](), *big.NewInt(100), ""},
		{"text refused", "6161", ptrTo[big.Int](), nil, "a text string into"},
	})
}
