package jsonenc_test

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/jsonenc"
)

// ExampleAppendFloat writes a million and a tenth of a micro as encoding/json
// does, and reports that NaN has no JSON number.
func ExampleAppendFloat() {
	out, _ := jsonenc.AppendFloat(nil, 1e6)
	out = append(out, ' ')
	out, _ = jsonenc.AppendFloat(out, 1e-7)
	_, ok := jsonenc.AppendFloat(nil, math.NaN())
	fmt.Println(string(out), ok)
	// Output:
	// 1000000 1e-7 false
}

// floatCorpus is the edge corpus: both zeros, the subnormals, the two cut-offs
// and their neighbours, the powers that change the digit count, 2^53 and the
// extremes.
var floatCorpus = []float64{
	0, math.Copysign(0, -1), 1, -1, 0.1, 0.5, 1.5, -2.25, 100, 1e6, -1e6,
	123456789, 1e-7, -1e-7, 1e-6, -1e-6, math.Nextafter(1e-6, 0), math.Nextafter(1e-6, 1),
	1e-5, 1e-9, 1.5e-8, 1e20, 1e21, -1e21, math.Nextafter(1e21, 0), math.Nextafter(1e21, math.Inf(1)),
	1e22, 1e100, 1e-100, 1 << 53, 1<<53 + 2, -(1 << 53), math.MaxFloat64, -math.MaxFloat64,
	math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64, 2.2250738585072014e-308,
	2.225073858507201e-308, math.Pi, math.E, 1.0 / 3, 2.0 / 3, 5e-324, 4.9406564584124654e-324,
	1e-10, 123e-20, 0.000001234, 9.999999999999999e20, 1.7976931348623157e308,
}

// checkFloat holds AppendFloat to json.Marshal for one finite value, and to its
// reading back to the same bits.
func checkFloat(t *testing.T, f float64) {
	t.Helper()
	want, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("json.Marshal(%v): %v", f, err)
	}
	got, ok := jsonenc.AppendFloat([]byte("pre"), f)
	if !ok {
		t.Fatalf("AppendFloat(%v) reported false for a finite value", f)
	}
	if string(got[len("pre"):]) != string(want) {
		t.Fatalf("AppendFloat(%v) = %s, json.Marshal = %s", f, got[len("pre"):], want)
	}
	var back float64
	if err := json.Unmarshal(got[len("pre"):], &back); err != nil || math.Float64bits(back) != math.Float64bits(f) {
		t.Fatalf("AppendFloat(%v) = %s reads back as %v (%v)", f, got[len("pre"):], back, err)
	}
}

// TestAppendFloatMatchesEncodingJSON holds every value of the edge corpus to
// the bytes json.Marshal writes for it.
func TestAppendFloatMatchesEncodingJSON(t *testing.T) {
	t.Parallel()
	for _, f := range floatCorpus {
		checkFloat(t, f)
	}
}

// TestAppendFloatSpelling pins the two places strconv's 'g' and encoding/json
// part: the plain range and the exponent's leading zero.
func TestAppendFloatSpelling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   float64
		want string
	}{
		{1e6, "1000000"},
		{1e-7, "1e-7"},
		{1e-6, "0.000001"},
		{1e20, "100000000000000000000"},
		{1e21, "1e+21"},
		{-1.5e-8, "-1.5e-8"},
		{math.Copysign(0, -1), "-0"},
		{1e-10, "1e-10"},
	}
	for _, tt := range tests {
		got, ok := jsonenc.AppendFloat(nil, tt.in)
		if !ok || string(got) != tt.want {
			t.Errorf("AppendFloat(%v) = %q, %v; want %q, true", tt.in, got, ok, tt.want)
		}
	}
}

// TestAppendFloatNonFinite: NaN and both infinities append nothing and report
// false, and the destination is handed back as it was.
func TestAppendFloatNonFinite(t *testing.T) {
	t.Parallel()
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		got, ok := jsonenc.AppendFloat([]byte("pre"), f)
		if ok || string(got) != "pre" {
			t.Errorf("AppendFloat(%v) = %q, %v; want %q, false", f, got, ok, "pre")
		}
	}
}

// TestObjectFloat writes a number, and null for the values JSON has no number for.
func TestObjectFloat(t *testing.T) {
	t.Parallel()
	out := jsonenc.BeginObject(nil).
		Float("a", 1e6).
		Float("b", 1e-7).
		Float("nan", math.NaN()).
		Float("inf", math.Inf(-1)).
		End()
	want := `{"a":1000000,"b":1e-7,"nan":null,"inf":null}`
	if string(out) != want {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
}

// FuzzAppendFloat holds AppendFloat to json.Marshal for any float64 bit
// pattern: a finite one is written byte for byte as Marshal writes it and reads
// back to the same bits, a non-finite one is refused.
func FuzzAppendFloat(f *testing.F) {
	for _, seed := range floatCorpus {
		f.Add(math.Float64bits(seed))
	}
	f.Add(math.Float64bits(math.NaN()))
	f.Add(math.Float64bits(math.Inf(1)))
	f.Fuzz(func(t *testing.T, bits uint64) {
		v := math.Float64frombits(bits)
		//: no JSON number: refused, nothing written.
		if math.IsNaN(v) || math.IsInf(v, 0) {
			if out, ok := jsonenc.AppendFloat(nil, v); ok || len(out) != 0 {
				t.Fatalf("AppendFloat(%v) = %q, %v", v, out, ok)
			}
			return
		}
		checkFloat(t, v)
	})
}
