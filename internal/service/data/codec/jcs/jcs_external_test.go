package jcs_test

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"

	corejcs "github.com/kitsunium/sdk/internal/core/data/codec/jcs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/jcs"
)

// TestRFC8785Example is the example of RFC 8785 §3.2.4: whitespace dropped,
// members sorted, numbers and strings respelled.
func TestRFC8785Example(t *testing.T) {
	t.Parallel()
	in := `{
  "numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001],
  "string": "%u20ac$%u000F%u000aA'%u0042%u0022%u005c\\\"\/",
  "literals": [null, true, false]
}`
	want := `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`
	//: the RFC's escapes, spelled %u here and turned into \u before reading.
	in = strings.ReplaceAll(in, "%u", `\`+"u")
	got, err := jcs.Canonicalize([]byte(in))
	if err != nil || string(got) != want {
		t.Fatalf("Canonicalize = %s, %v\nwant %s", got, err, want)
	}
}

// TestRFC8785SortsByUTF16 is §3.2.3's sorting example: names ordered by their
// UTF-16 code units, so an emoji (a surrogate pair) sorts before U+FB33.
func TestRFC8785SortsByUTF16(t *testing.T) {
	t.Parallel()
	in := `{"€":"Euro Sign","\r":"Carriage Return","דּ":"Hebrew Letter Dalet With Dagesh","1":"One","😀":"Emoji: Grinning Face","\u0080":"Control","ö":"Latin Small Letter O With Diaeresis"}`
	got, err := jcs.Canonicalize([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"Carriage Return", "One", "Control", "Latin Small Letter O With Diaeresis", "Euro Sign", "Emoji: Grinning Face", "Hebrew Letter Dalet With Dagesh"}
	last := -1
	for _, v := range order {
		i := strings.Index(string(got), v)
		if i <= last {
			t.Fatalf("%q out of order in %s", v, got)
		}
		last = i
	}
}

// TestRFC8785Numbers is Appendix B: each IEEE 754 double, given by its bits,
// written as ECMAScript writes it; NaN and Infinity have no form.
func TestRFC8785Numbers(t *testing.T) {
	t.Parallel()
	vectors := map[string]string{
		"0000000000000000": "0",
		"8000000000000000": "0",
		"0000000000000001": "5e-324",
		"8000000000000001": "-5e-324",
		"7fefffffffffffff": "1.7976931348623157e+308",
		"ffefffffffffffff": "-1.7976931348623157e+308",
		"4340000000000000": "9007199254740992",
		"c340000000000000": "-9007199254740992",
		"4430000000000000": "295147905179352830000",
		"44b52d02c7e14af5": "9.999999999999997e+22",
		"44b52d02c7e14af6": "1e+23",
		"44b52d02c7e14af7": "1.0000000000000001e+23",
		"444b1ae4d6e2ef4e": "999999999999999700000",
		"444b1ae4d6e2ef4f": "999999999999999900000",
		"444b1ae4d6e2ef50": "1e+21",
		"3eb0c6f7a0b5ed8c": "9.999999999999997e-7",
		"3eb0c6f7a0b5ed8d": "0.000001",
		"41b3de4355555553": "333333333.3333332",
		"41b3de4355555554": "333333333.33333325",
		"41b3de4355555555": "333333333.3333333",
		"41b3de4355555556": "333333333.3333334",
		"41b3de4355555557": "333333333.33333343",
		"becbf647612f3696": "-0.0000033333333333333333",
		"43143ff3c1cb0959": "1424953923781206.2",
	}
	for bits, want := range vectors {
		raw, _ := hex.DecodeString(bits)
		f := math.Float64frombits(binary.BigEndian.Uint64(raw))
		in, err := jcs.Marshal(f)
		if err != nil {
			t.Fatalf("%s: Marshal: %v", bits, err)
		}
		if string(in) != want {
			t.Fatalf("%s: wrote %s, want %s", bits, in, want)
		}
		again, err := jcs.Canonicalize([]byte(want))
		if err != nil || string(again) != want {
			t.Fatalf("%s: the canonical form is not a fixed point: %s, %v", bits, again, err)
		}
	}
	for _, f := range []float64{math.NaN(), math.Inf(1)} {
		if _, err := jcs.Marshal(f); !errs.HasCode(err, corejcs.CodeJCSMarshalFailed) {
			t.Fatalf("%v: %v, want MARSHAL_FAILED", f, err)
		}
	}
}

// TestCanonicalizeRefusesWithoutQuoting pins the refusals — not I-JSON,
// trailing data, a duplicate name, invalid UTF-8, a number past the largest
// double — and that none quotes the document.
func TestCanonicalizeRefusesWithoutQuoting(t *testing.T) {
	t.Parallel()
	cases := map[string]errs.Code{
		`{"secret-value":1,"secret-value":2}`: corejcs.CodeJCSNotJSON,
		`["secret-value"] trailing`:           corejcs.CodeJCSNotJSON,
		"[\"secret-value\xff\"]":              corejcs.CodeJCSNotJSON,
		`{"secret-value":`:                    corejcs.CodeJCSNotJSON,
		``:                                    corejcs.CodeJCSNotJSON,
		`[1e400,"secret-value"]`:              corejcs.CodeJCSNumberInvalid,
	}
	for in, code := range cases {
		_, err := jcs.Canonicalize([]byte(in))
		if !errs.HasCode(err, code) {
			t.Fatalf("%q: %v, want %v", in, err, code)
		}
		var e *errs.Error
		if errors.As(err, &e) && (strings.Contains(e.Private(), "secret") || strings.Contains(err.Error(), "secret")) {
			t.Fatalf("%q: the refusal quotes the document: %v", in, err)
		}
	}
}

// TestCanonicalizeStrings pins §3.2.2.2: only '"', '\' and controls escaped,
// the five named ones by name, the others in lower-case hex; U+2028, '<' and
// '/' as themselves.
func TestCanonicalizeStrings(t *testing.T) {
	t.Parallel()
	in := `"\u0000\u001f\b\t\n\f\r\"\\\/<>& é😀"`
	want := "\"\\u0000\\u001f\\b\\t\\n\\f\\r\\\"\\\\/<>& é😀\""
	if got, err := jcs.Canonicalize([]byte(in)); err != nil || string(got) != want {
		t.Fatalf("Canonicalize = %q, %v; want %q", got, err, want)
	}
}

// TestCanonicalizeIsIdempotentOnNesting pins that a nested document is
// sorted at every depth and that canonicalizing the canonical form is a
// fixed point.
func TestCanonicalizeIsIdempotentOnNesting(t *testing.T) {
	t.Parallel()
	in := `{"b":[{"z":1,"a":{"y":[],"x":{}}},-0.0,1.0],"a":"x"}`
	want := `{"a":"x","b":[{"a":{"x":{},"y":[]},"z":1},0,1]}`
	got, err := jcs.Canonicalize([]byte(in))
	if err != nil || string(got) != want {
		t.Fatalf("Canonicalize = %s, %v; want %s", got, err, want)
	}
	if again, _ := jcs.Canonicalize(got); string(again) != want {
		t.Fatalf("not a fixed point: %s", again)
	}
}
