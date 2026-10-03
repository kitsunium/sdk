// Package bson_test — the value types as a caller uses them: their textual and
// JSON forms, their comparisons, and the decimal128 conversions, checked
// against vectors from the BSON decimal128 specification's test corpus.
package bson_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/bson"
)

// TestObjectID pins the hexadecimal, textual and JSON forms.
func TestObjectID(t *testing.T) {
	t.Parallel()
	id := bson.ObjectID{0x65, 0x10, 0x20, 0x30, 1, 2, 3, 4, 5, 6, 7, 8}
	type tc struct {
		name  string
		check func(t *testing.T)
	}
	tests := []tc{
		{"Hex and String", func(t *testing.T) {
			t.Helper()
			//: lowercase, 24 digits, wrapped by String.
			if id.Hex() != "651020300102030405060708" || id.String() != `ObjectID("651020300102030405060708")` {
				t.Errorf("Hex = %s, String = %s", id.Hex(), id.String())
			}
		}},
		{"FromHex accepts either case and refuses anything else", func(t *testing.T) {
			t.Helper()
			got, err := bson.ObjectIDFromHex("651020300102030405060708")
			if err != nil || got != id {
				t.Fatalf("ObjectIDFromHex = %v, %v", got, err)
			}
			if upper, err := bson.ObjectIDFromHex("ABCDEF000000000000000000"); err != nil || upper[0] != 0xAB {
				t.Errorf("upper case = %v, %v", upper, err)
			}
			//: a wrong length and a non-digit.
			for _, bad := range []string{"", "65102030010203040506070", "zz1020300102030405060708"} {
				if _, err := bson.ObjectIDFromHex(bad); !errs.HasCode(err, bson.CodeBSONValueInvalid) {
					t.Errorf("ObjectIDFromHex(%q) = %v, want BSON_VALUE_INVALID", bad, err)
				}
			}
		}},
		{"Timestamp is the first four bytes", func(t *testing.T) {
			t.Helper()
			want := time.Unix(0x65102030, 0).UTC()
			if !id.Timestamp().Equal(want) || id.Timestamp().Location() != time.UTC {
				t.Errorf("Timestamp = %v, want %v", id.Timestamp(), want)
			}
		}},
		{"IsZero", func(t *testing.T) {
			t.Helper()
			if !bson.NilObjectID.IsZero() || id.IsZero() {
				t.Error("IsZero disagrees with the nil identifier")
			}
		}},
		{"JSON is the hexadecimal string, both shapes read", func(t *testing.T) {
			t.Helper()
			out, err := json.Marshal(map[string]bson.ObjectID{"id": id})
			if err != nil || string(out) != `{"id":"651020300102030405060708"}` {
				t.Fatalf("json.Marshal = %s, %v", out, err)
			}
			var plain, wrapped, empty bson.ObjectID
			if err := json.Unmarshal([]byte(`"651020300102030405060708"`), &plain); err != nil || plain != id {
				t.Errorf("plain = %v, %v", plain, err)
			}
			if err := json.Unmarshal([]byte(`{"$oid":"651020300102030405060708"}`), &wrapped); err != nil || wrapped != id {
				t.Errorf("$oid = %v, %v", wrapped, err)
			}
			if err := json.Unmarshal([]byte(`""`), &empty); err != nil || !empty.IsZero() {
				t.Errorf("empty = %v, %v", empty, err)
			}
			var refused bson.ObjectID
			if err := json.Unmarshal([]byte(`42`), &refused); err == nil {
				t.Error("a number decoded into an ObjectID")
			}
		}},
		{"a text map key", func(t *testing.T) {
			t.Helper()
			out, err := json.Marshal(map[bson.ObjectID]int{id: 1})
			if err != nil || string(out) != `{"651020300102030405060708":1}` {
				t.Fatalf("json.Marshal = %s, %v", out, err)
			}
			back := map[bson.ObjectID]int{}
			if err := json.Unmarshal(out, &back); err != nil || back[id] != 1 {
				t.Errorf("json.Unmarshal = %v, %v", back, err)
			}
		}},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.check(t)
		})
	}
}

// TestDateTime pins the conversion to and from time.Time, before the epoch
// included, and the JSON form.
func TestDateTime(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		at   time.Time
		want bson.DateTime
	}
	tests := []tc{
		{"the epoch", time.Unix(0, 0), 0},
		{"after it, truncated to the millisecond", time.Unix(1, 999_999_999), 1999},
		{"half a second before it", time.Unix(0, -500_000_000), -500},
		{"a millisecond and a half before it", time.Unix(0, -1_500_000), -2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := bson.NewDateTimeFromTime(c.at)
		if got != c.want {
			t.Fatalf("NewDateTimeFromTime = %d, want %d", got, c.want)
		}
		//: back to the instant, at millisecond precision.
		if !got.Time().Equal(time.UnixMilli(int64(c.want))) {
			t.Errorf("Time = %v, want %v", got.Time(), time.UnixMilli(int64(c.want)))
		}
		out, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("json.Marshal = %v", err)
		}
		var back bson.DateTime
		if err := json.Unmarshal(out, &back); err != nil || back != got {
			t.Errorf("JSON round trip %s = %d, %v", out, back, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDecimal128String pins the string form against the specification's
// corpus: the BID encoding on the left, the canonical string on the right.
func TestDecimal128String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		high, low uint64
		want      string
	}
	tests := []tc{
		{"zero", 0x3040000000000000, 0, "0"},
		{"negative zero", 0xB040000000000000, 0, "-0"},
		{"one", 0x3040000000000000, 1, "1"},
		{"negative one", 0xB040000000000000, 1, "-1"},
		{"a fraction", 0x303C000000000000, 123, "1.23"},
		{"plain down to the adjusted exponent -6", 0x3034000000000000, 1, "0.000001"},
		{"scientific below it", 0x3032000000000000, 1, "1E-7"},
		{"a positive exponent is scientific", 0x3046000000000000, 1, "1E+3"},
		{"zero with an exponent", 0x3046000000000000, 0, "0E+3"},
		{"zero with a negative exponent", 0x303C000000000000, 0, "0.00"},
		{"the largest coefficient", 0x3041ED09BEAD87C0, 0x378D8E63FFFFFFFF, "9999999999999999999999999999999999"},
		{"the smallest exponent", 0x0000000000000000, 1, "1E-6176"},
		{"the largest exponent", 0x5FFE000000000000, 1, "1E+6111"},
		{"a coefficient past 10^34-1 reads as zero", 0x6C10000000000000, 0, "0"},
		{"the large form reads as zero with its exponent", 0x6C11FFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF, "0E+3"},
		{"NaN", 0x7C00000000000000, 0, "NaN"},
		{"a negative signalling NaN", 0xFE00000000000000, 0, "NaN"},
		{"Infinity", 0x7800000000000000, 0, "Infinity"},
		{"-Infinity", 0xF800000000000000, 0, "-Infinity"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := bson.NewDecimal128(c.high, c.low).String(); got != c.want {
			t.Errorf("String = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestParseDecimal128 pins the parser: exact or refused, never rounded.
func TestParseDecimal128(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		in        string
		high, low uint64
		refused   bool
	}
	tests := []tc{
		{"an integer", "1", 0x3040000000000000, 1, false},
		{"a fraction", "1.23", 0x303C000000000000, 123, false},
		{"a leading point", ".5", 0x303E000000000000, 5, false},
		{"a trailing point", "5.", 0x3040000000000000, 5, false},
		{"an exponent", "1e3", 0x3046000000000000, 1, false},
		{"a signed exponent", "-1.00E-8", 0xB02C000000000000, 100, false},
		{"leading zeros", "0001", 0x3040000000000000, 1, false},
		{"negative zero", "-0", 0xB040000000000000, 0, false},
		{"zero clamped into range", "0E+99999", 0x5FFE000000000000, 0, false},
		{"trailing zeros traded for exponent", "10000000000000000000000000000000000000E-4", 0x3040314DC6448D93, 0x38C15B0A00000000, false},
		{"clamped by appending zeros", "1E+6112", 0x5FFE000000000000, 10, false},
		{"NaN in any case", "nAn", 0x7C00000000000000, 0, false},
		{"Infinity", "+inf", 0x7800000000000000, 0, false},
		{"-Infinity", "-Infinity", 0xF800000000000000, 0, false},
		{"empty", "", 0, 0, true},
		{"a point alone", ".", 0, 0, true},
		{"two points", "1.2.3", 0, 0, true},
		{"an incomplete exponent", "1e", 0, 0, true},
		{"white space", " 1", 0, 0, true},
		{"two signs", "+-1", 0, 0, true},
		{"35 significant digits", "1.1111111111111111111111111111111234", 0, 0, true},
		{"past the largest exponent", "7e10000", 0, 0, true},
		{"below the smallest exponent", "1E-6177", 0, 0, true},
		{"a near special", "Infinit", 0, 0, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := bson.ParseDecimal128(c.in)
		if c.refused {
			if !errs.HasCode(err, bson.CodeBSONValueInvalid) {
				t.Fatalf("ParseDecimal128(%q) = %v, %v, want BSON_VALUE_INVALID", c.in, got, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("ParseDecimal128(%q) = %v", c.in, err)
		}
		high, low := got.GetBytes()
		if high != c.high || low != c.low {
			t.Errorf("ParseDecimal128(%q) = %#x %#x, want %#x %#x", c.in, high, low, c.high, c.low)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDecimal128RoundTrip pins that the string form parses back to the same
// value for every finite encoding the parser produces.
func TestDecimal128RoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
	}
	tests := []tc{
		{"a long coefficient", "1234567890123456789012345678901234"},
		{"a long coefficient scaled", "1.234567890123456789012345678901234E+6111"},
		{"a small one", "1E-6176"},
		{"a negative fraction", "-0.000001"},
		{"many trailing zeros", "1000000000000000000000000000000000000000"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		first, err := bson.ParseDecimal128(c.in)
		if err != nil {
			t.Fatalf("ParseDecimal128(%q) = %v", c.in, err)
		}
		again, err := bson.ParseDecimal128(first.String())
		if err != nil || again != first {
			t.Errorf("String %q parsed back to %v, %v", first.String(), again, err)
		}
		out, err := json.Marshal(first)
		if err != nil {
			t.Fatalf("json.Marshal = %v", err)
		}
		var viaJSON, viaExtended bson.Decimal128
		if err := json.Unmarshal(out, &viaJSON); err != nil || viaJSON != first {
			t.Errorf("JSON %s read back %v, %v", out, viaJSON, err)
		}
		ext := `{"$numberDecimal":` + string(out) + `}`
		if err := json.Unmarshal([]byte(ext), &viaExtended); err != nil || viaExtended != first {
			t.Errorf("extended JSON %s read back %v, %v", ext, viaExtended, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDecimal128Predicates pins IsNaN, IsInf and IsZero — the last being the
// zero value of the type, not numeric zero.
func TestDecimal128Predicates(t *testing.T) {
	t.Parallel()
	parse := func(s string) bson.Decimal128 {
		d, err := bson.ParseDecimal128(s)
		//: the fixtures are well formed.
		if err != nil {
			t.Fatalf("ParseDecimal128(%q) = %v", s, err)
		}
		return d
	}
	nan, inf, zero := parse("NaN"), parse("-Infinity"), parse("0")
	//: NaN.
	if !nan.IsNaN() || nan.IsInf() != 0 {
		t.Error("NaN misclassified")
	}
	//: an infinity.
	if inf.IsNaN() || inf.IsInf() != -1 {
		t.Error("-Infinity misclassified")
	}
	//: "0" has exponent 0, so its bytes are not all zero.
	if zero.IsZero() || !(bson.Decimal128{}).IsZero() {
		t.Error("IsZero is not the zero value's test")
	}
}

// TestTimestampOrder pins the comparison: seconds first, increment second.
func TestTimestampOrder(t *testing.T) {
	t.Parallel()
	a := bson.Timestamp{T: 1, I: 5}
	b := bson.Timestamp{T: 2, I: 0}
	c := bson.Timestamp{T: 2, I: 1}
	//: every relation.
	if !a.Before(b) || !c.After(b) || a.Compare(b) != -1 || c.Compare(b) != 1 || b.Compare(b) != 0 || !b.Equal(b) {
		t.Error("Timestamp ordering is wrong")
	}
	//: the zero value.
	if !(bson.Timestamp{}).IsZero() || a.IsZero() {
		t.Error("IsZero is wrong")
	}
}

// TestValueTypeMethods pins the small methods of the remaining value types.
func TestValueTypeMethods(t *testing.T) {
	t.Parallel()
	bin := bson.Binary{Subtype: bson.BinaryUUID, Data: []byte{1}}
	rx := bson.Regex{Pattern: "p", Options: "i"}
	ptr := bson.DBPointer{DB: "d", Pointer: bson.ObjectID{1}}
	//: Binary.
	if !bin.Equal(bson.Binary{Subtype: bson.BinaryUUID, Data: []byte{1}}) || bin.IsZero() || !(bson.Binary{}).IsZero() {
		t.Error("Binary methods are wrong")
	}
	//: Regex.
	if !rx.Equal(bson.Regex{Pattern: "p", Options: "i"}) || rx.IsZero() || rx.String() != `{"pattern": "p", "options": "i"}` {
		t.Errorf("Regex methods are wrong: %s", rx.String())
	}
	//: DBPointer.
	if !ptr.Equal(ptr) || ptr.IsZero() || !(bson.DBPointer{}).IsZero() {
		t.Error("DBPointer methods are wrong")
	}
}
