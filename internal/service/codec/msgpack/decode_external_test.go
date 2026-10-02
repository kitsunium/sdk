package msgpack_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/msgpack"
)

// Decode fixtures.
type (
	// pair is a two-field struct.
	pair struct {
		A int    `msgpack:"A"`
		B string `msgpack:"B"`
	}
	// everyKind has one field of every kind nil can zero.
	everyKind struct {
		I  int            `msgpack:"i"`
		U  uint16         `msgpack:"u"`
		F  float32        `msgpack:"f"`
		S  string         `msgpack:"s"`
		Bo bool           `msgpack:"bo"`
		By []byte         `msgpack:"by"`
		Ar [2]byte        `msgpack:"ar"`
		Li []int          `msgpack:"li"`
		Ma map[string]int `msgpack:"ma"`
		Pt *int           `msgpack:"pt"`
		St pair           `msgpack:"st"`
		Ti time.Time      `msgpack:"ti"`
		An any            `msgpack:"an"`
		Ia [2]int         `msgpack:"ia"`
	}
	// withError carries an error-typed field.
	withError struct {
		E error `msgpack:"e"`
	}
	// rawCapture keeps the bytes UnmarshalMsgpack received.
	rawCapture struct {
		got []byte
	}
	// textCapture records UnmarshalText calls.
	textCapture struct {
		text  string
		calls int
	}
	// failingText fails in UnmarshalText.
	failingText struct{}
)

// UnmarshalMsgpack keeps what it receives.
func (r *rawCapture) UnmarshalMsgpack(b []byte) error {
	r.got = b
	return nil
}

// UnmarshalText records the text.
func (c *textCapture) UnmarshalText(b []byte) error {
	c.text = string(b)
	c.calls++
	return nil
}

// UnmarshalText fails.
func (*failingText) UnmarshalText([]byte) error { return errors.New("refused") }

// fromHex decodes a hex fixture, ignoring spaces.
func fromHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatalf("bad fixture %q: %v", s, err)
	}
	return b
}

// nested returns n fixarray-of-one headers followed by nil.
func nested(n int) []byte {
	return append(bytesOf(0x91, n), 0xc0)
}

// bytesOf repeats b n times.
func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// TestUnmarshalHostile feeds malformed, truncated, oversized and
// out-of-range inputs: every one is UNMARSHAL_FAILED with a routable code,
// and none panics.
func TestUnmarshalHostile(t *testing.T) {
	t.Parallel()
	var (
		asAny   any
		asInt8  int8
		asUint8 uint8
		asInt64 int64
		asF32   float32
		asInt   int
		asMap   map[string]int
		asAnyM  map[any]int
		asArr   [2]int
		asBytes [4]byte
		asPair  pair
		asTime  time.Time
		asStr   fmt.Stringer
		asChan  chan int
	)
	tests := []struct {
		name   string
		input  []byte
		target any
	}{
		{"empty input", nil, &asAny},
		{"never-used byte", []byte{0xc1}, &asAny},
		{"fixstr longer than input", fromHex(t, "a5 61 62"), &asAny},
		{"str 32 of 4 GiB", fromHex(t, "db ffffffff"), &asAny},
		{"bin 32 of 4 GiB", fromHex(t, "c6 ffffffff"), &asAny},
		{"array 32 of 4 billion", fromHex(t, "dd ffffffff"), &asAny},
		{"array 32 into a slice", fromHex(t, "dd ffffffff"), new([]int)},
		{"map 32 of 4 billion", fromHex(t, "df ffffffff"), &asAny},
		{"map 32 into a map", fromHex(t, "df ffffffff"), &asMap},
		{"ext 32 of 4 GiB", fromHex(t, "c9 ffffffff 01"), &asAny},
		{"truncated uint 16", fromHex(t, "cd 01"), &asAny},
		{"fixext without type", fromHex(t, "d4"), &asAny},
		{"array missing an element", fromHex(t, "92 01"), &asAny},
		{"map missing a value", fromHex(t, "81 a1 61"), &asAny},
		{"1001 nested arrays", nested(1001), &asAny},
		{"trailing byte", fromHex(t, "01 02"), &asAny},
		{"timestamp nanoseconds past 999999999", fromHex(t, "d7 ff ee6b2800 00000000"), &asAny},
		{"timestamp of length 2", fromHex(t, "d5 ff 0000"), &asAny},
		{"unknown extension into any", fromHex(t, "d4 05 00"), &asAny},
		{"non-string key into any", fromHex(t, "81 01 01"), &asAny},
		{"300 into int8", fromHex(t, "cd 012c"), &asInt8},
		{"-1 into uint8", fromHex(t, "ff"), &asUint8},
		{"uint 64 max into int64", fromHex(t, "cf ffffffffffffffff"), &asInt64},
		{"1e40 into float32", fromHex(t, "cb 483d6329f1c35ca5"), &asF32},
		{"float into int", fromHex(t, "cb 3ff8000000000000"), &asInt},
		{"string into int", fromHex(t, "a1 61"), &asInt},
		{"integer key into map[string]int", fromHex(t, "81 01 01"), &asMap},
		{"array key into map[any]int", fromHex(t, "81 9101 01"), &asAnyM},
		{"map key into map[any]int", fromHex(t, "81 80 01"), &asAnyM},
		{"three elements into [2]int", fromHex(t, "93 010203"), &asArr},
		{"five bytes into [4]byte", fromHex(t, "c4 05 0102030405"), &asBytes},
		{"array of 1 into a 2-field struct", fromHex(t, "91 01"), &asPair},
		{"integer key into a struct", fromHex(t, "81 01 01"), &asPair},
		{"application extension into time", fromHex(t, "d6 05 00000000"), &asTime},
		{"non-RFC 3339 string into time", fromHex(t, "a3 616263"), &asTime},
		{"string into a non-empty interface", fromHex(t, "a1 61"), &asStr},
		{"into a channel", fromHex(t, "c0"), &asChan},
		{"into a non-pointer", fromHex(t, "01"), asInt},
		{"into a nil pointer", fromHex(t, "01"), (*int)(nil)},
		{"into nil", fromHex(t, "01"), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := msgpack.New().Unmarshal(tc.input, tc.target)
			if !errs.HasReason(err, "UNMARSHAL_FAILED") {
				t.Fatalf("want UNMARSHAL_FAILED, got %v", err)
			}
			if code, ok := errs.CodeOf(err); !ok || code != msgpack.CodeMsgPackUnmarshalFailed {
				t.Fatalf("want code %s, got %v", msgpack.CodeMsgPackUnmarshalFailed, err)
			}
		})
	}
}

// TestUnmarshalNeverPanicsOnDamage decodes every prefix of a rich encoding,
// and the encoding with each byte overwritten, into a typed target and into
// any: truncation always fails, damage never panics.
func TestUnmarshalNeverPanicsOnDamage(t *testing.T) {
	t.Parallel()
	c := msgpack.New()
	full := mustMarshal(t, benchRecordOf(3))
	for i := range len(full) {
		var rec benchRecord
		if err := c.Unmarshal(full[:i], &rec); err == nil {
			t.Fatalf("prefix of %d bytes decoded without error", i)
		}
		var v any
		if err := c.Unmarshal(full[:i], &v); err == nil {
			t.Fatalf("prefix of %d bytes decoded into any without error", i)
		}
	}
	damaged := make([]byte, len(full))
	for i := range len(full) {
		for _, b := range []byte{0x00, 0x7f, 0x91, 0xc1, 0xcf, 0xdd, 0xff} {
			copy(damaged, full)
			damaged[i] = b
			var rec benchRecord
			checkFailure(t, c.Unmarshal(damaged, &rec))
			var v any
			checkFailure(t, c.Unmarshal(damaged, &v))
		}
	}
}

// TestUnmarshalNilZeroes sets every kind of field to its zero from nil.
func TestUnmarshalNilZeroes(t *testing.T) {
	t.Parallel()
	dst := everyKind{
		I: 1, U: 2, F: 3, S: "s", Bo: true, By: []byte{1}, Ar: [2]byte{1, 2}, Li: []int{1},
		Ma: map[string]int{"a": 1}, Pt: new(7), St: pair{A: 1}, Ti: time.Now(), An: "x", Ia: [2]int{1, 2},
	}
	keys := []string{"i", "u", "f", "s", "bo", "by", "ar", "li", "ma", "pt", "st", "ti", "an", "ia"}
	input := []byte{0x80 | byte(len(keys))}
	for _, k := range keys {
		input = append(append(append(input, 0xa0|byte(len(k))), k...), 0xc0)
	}
	if err := msgpack.New().Unmarshal(input, &dst); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(dst, everyKind{}) {
		t.Fatalf("not zeroed: %+v", dst)
	}
}

// TestUnmarshalIntoExisting pins what a decode does to a target that already
// holds data.
func TestUnmarshalIntoExisting(t *testing.T) {
	t.Parallel()
	c := msgpack.New()
	t.Run("slice is replaced, its array reused", func(t *testing.T) {
		dst := []int{9, 9, 9, 9}
		before := &dst[0]
		if err := c.Unmarshal(mustMarshal(t, []int{1, 2}), &dst); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(dst, []int{1, 2}) || &dst[0] != before {
			t.Fatalf("got %v (reused=%v)", dst, &dst[0] == before)
		}
	})
	t.Run("slice of pointers does not mutate what it held", func(t *testing.T) {
		held := &pair{A: 1}
		dst := []*pair{held}
		if err := c.Unmarshal(mustMarshal(t, []pair{{A: 2}}), &dst); err != nil {
			t.Fatal(err)
		}
		if held.A != 1 || dst[0].A != 2 {
			t.Fatalf("held.A=%d dst[0].A=%d", held.A, dst[0].A)
		}
	})
	t.Run("array tail is zeroed", func(t *testing.T) {
		dst := [3]int{7, 7, 7}
		if err := c.Unmarshal(mustMarshal(t, []int{1}), &dst); err != nil {
			t.Fatal(err)
		}
		if dst != [3]int{1, 0, 0} {
			t.Fatalf("got %v", dst)
		}
	})
	t.Run("map gains pairs", func(t *testing.T) {
		dst := map[string]int{"keep": 1}
		if err := c.Unmarshal(mustMarshal(t, map[string]int{"new": 2}), &dst); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(dst, map[string]int{"keep": 1, "new": 2}) {
			t.Fatalf("got %v", dst)
		}
	})
	t.Run("struct keeps fields the input does not name", func(t *testing.T) {
		dst := pair{A: 1, B: "b"}
		if err := c.Unmarshal(mustMarshal(t, map[string]int{"A": 5}), &dst); err != nil {
			t.Fatal(err)
		}
		if dst != (pair{A: 5, B: "b"}) {
			t.Fatalf("got %+v", dst)
		}
	})
	t.Run("interface holding a pointer is decoded into", func(t *testing.T) {
		held := &pair{}
		var dst any = held
		if err := c.Unmarshal(mustMarshal(t, pair{A: 3}), &dst); err != nil {
			t.Fatal(err)
		}
		if dst != any(held) || held.A != 3 {
			t.Fatalf("got %#v, held %+v", dst, held)
		}
	})
	t.Run("interface holding a value is replaced", func(t *testing.T) {
		var dst any = 5
		if err := c.Unmarshal(mustMarshal(t, "s"), &dst); err != nil {
			t.Fatal(err)
		}
		if dst != "s" {
			t.Fatalf("got %#v", dst)
		}
	})
	t.Run("nil clears an interface holding a pointer", func(t *testing.T) {
		var dst any = &pair{}
		if err := c.Unmarshal([]byte{0xc0}, &dst); err != nil {
			t.Fatal(err)
		}
		if dst != nil {
			t.Fatalf("got %#v", dst)
		}
	})
}

// TestUnmarshalAcrossForms decodes each target from every form that may
// carry it, including the non-shortest forms another encoder may write.
func TestUnmarshalAcrossForms(t *testing.T) {
	t.Parallel()
	c := msgpack.New()
	tests := []struct {
		name  string
		input string
		want  any
	}{
		{"int8 from int 64", "d3 0000000000000005", int8(5)},
		{"uint from int 8", "d0 05", uint(5)},
		{"int from uint 32", "ce 00010000", 65536},
		{"float32 from int", "d0 fb", float32(-5)},
		{"float32 from float 64 in range", "cb 3ff8000000000000", float32(1.5)},
		{"float64 from uint 64 past MaxInt64", "cf 8000000000000000", float64(1 << 63)},
		{"string from bin", "c4 02 6869", "hi"},
		{"bytes from str", "a2 6869", []byte("hi")},
		{"string from str 8 short", "d9 02 6869", "hi"},
		{"bool from nil", "c0", false},
		{"int16 from fixint", "7f", int16(127)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := reflect.New(reflect.TypeOf(tc.want))
			if err := c.Unmarshal(fromHex(t, tc.input), target.Interface()); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got := target.Elem().Interface(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestUnmarshalUntypedKeys reads bin and nil keys of an untyped map as
// strings, as the vendor-backed codec did.
func TestUnmarshalUntypedKeys(t *testing.T) {
	t.Parallel()
	var got any
	if err := msgpack.New().Unmarshal(fromHex(t, "82 c4016b 01 c0 02"), &got); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"k": int8(1), "": int8(2)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
}

// TestUnmarshalErrorField turns a string into an error carrying it.
func TestUnmarshalErrorField(t *testing.T) {
	t.Parallel()
	var got withError
	if err := msgpack.New().Unmarshal(mustMarshal(t, withError{E: errors.New("boom")}), &got); err != nil {
		t.Fatal(err)
	}
	if got.E == nil || got.E.Error() != "boom" {
		t.Fatalf("got %v", got.E)
	}
}

// TestUnmarshalMethods covers the decode methods: each receives a copy of
// its bytes, nil zeroes without calling, a failure is wrapped.
func TestUnmarshalMethods(t *testing.T) {
	t.Parallel()
	c := msgpack.New()
	t.Run("UnmarshalMsgpack gets a copy of exactly one value", func(t *testing.T) {
		input := mustMarshal(t, []any{[]any{1, "x"}, true})
		var got []rawCapture
		if err := c.Unmarshal(input, &got); err != nil {
			t.Fatal(err)
		}
		want := mustMarshal(t, []any{1, "x"})
		input[2] = 0xff
		if len(got) != 2 || string(got[0].got) != string(want) || string(got[1].got) != "\xc3" {
			t.Fatalf("got %x / %x", got[0].got, got[1].got)
		}
	})
	t.Run("UnmarshalText from str and bin", func(t *testing.T) {
		for _, in := range []string{"a3 616263", "c4 03 616263"} {
			var got textCapture
			if err := c.Unmarshal(fromHex(t, in), &got); err != nil || got.text != "abc" {
				t.Fatalf("%s: got %q err=%v", in, got.text, err)
			}
		}
	})
	t.Run("nil zeroes without calling", func(t *testing.T) {
		got := textCapture{text: "old"}
		if err := c.Unmarshal([]byte{0xc0}, &got); err != nil || got != (textCapture{}) {
			t.Fatalf("got %+v err=%v", got, err)
		}
	})
	t.Run("a method failure is UNMARSHAL_FAILED", func(t *testing.T) {
		var got failingText
		if err := c.Unmarshal(fromHex(t, "a1 61"), &got); !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Fatalf("got %v", err)
		}
	})
}

// TestUnmarshalDeclaredCountReservesLittle declares 65 536 elements of 1 KiB
// each, which the input can hold one byte apiece, and breaks the FIRST one:
// the decode must fail having reserved a bounded amount, not the 64 MiB the
// count asks for. No t.Parallel: it reads the process-wide allocation counter.
func TestUnmarshalDeclaredCountReservesLittle(t *testing.T) {
	const (
		count    int    = 64 << 10
		reserved uint64 = 1 << 20
	)
	input := append(append(fromHex(t, "dd 00010000"), 0xc1), bytesOf(0xc0, count-1)...)
	var got [][1024]byte
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := msgpack.New().Unmarshal(input, &got)
	runtime.ReadMemStats(&after)
	if !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Fatalf("want UNMARSHAL_FAILED, got %v", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > reserved {
		t.Fatalf("allocated %d bytes for a decode that failed on its first element", grew)
	}
}
