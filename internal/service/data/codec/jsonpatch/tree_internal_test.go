// Package jsonpatch — the one branch no JSON reaches: a number whose exponent
// is not an integer, which jsontext never accepts.
package jsonpatch

import "testing"

// TestANumberWithoutAnIntegerExponentIsNoNumber pins the refusal a number
// jsontext accepted never meets: canonical says so, and sameNumber compares
// such a text only as written.
func TestANumberWithoutAnIntegerExponentIsNoNumber(t *testing.T) {
	t.Parallel()
	if _, _, _, ok := canonical("1e+x"); ok {
		t.Fatal("canonical read an exponent that is not an integer")
	}
	if sameNumber("1e+x", "1e+y") || !sameNumber("1e+x", "1e+x") {
		t.Fatal("texts that are not numbers are compared as written")
	}
	if negative, digits, exponent, ok := canonical("-0.0150e+3"); !ok || !negative || digits != "15" || exponent.Int64() != 0 {
		t.Fatalf("canonical(-0.0150e+3) = %v %q %v %v, want -, 15, 10^0", negative, digits, exponent, ok)
	}
}

// TestEqualValuesShareADigest pins the property equal rests on: two values
// equal compares equal carry the same digest — members in any order, a number
// however written, a string however escaped — and values that differ in
// one byte do not.
func TestEqualValuesShareADigest(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]string{
		{`{"a":1,"b":[true,null,"x"]}`, `{"b":[true,null,"x"],"a":1.0}`},
		{`"caf\u00e9"`, `"café"`},
		{`[-1.5e3, 0, 12]`, `[-1500, -0.0, 1.2e1]`},
	} {
		a, err := parse("from", []byte(pair[0]))
		if err != nil {
			t.Fatal(err)
		}
		b, err := parse("to", []byte(pair[1]))
		if err != nil {
			t.Fatal(err)
		}
		if !equal(a, b) || a.sum != b.sum {
			t.Fatalf("%s and %s: equal %v, digests %x and %x", pair[0], pair[1], equal(a, b), a.sum, b.sum)
		}
	}
	for _, pair := range [][2]string{
		{`{"a":1}`, `{"a":2}`},
		{`{"a":1,"b":2}`, `{"a":2,"b":1}`},
		{`["a","b"]`, `["b","a"]`},
		{`["ab"]`, `["a","b"]`},
		{`1`, `-1`},
	} {
		a, err := parse("from", []byte(pair[0]))
		if err != nil {
			t.Fatal(err)
		}
		b, err := parse("to", []byte(pair[1]))
		if err != nil {
			t.Fatal(err)
		}
		if equal(a, b) || a.sum == b.sum {
			t.Fatalf("%s and %s: equal %v, digests %x and %x", pair[0], pair[1], equal(a, b), a.sum, b.sum)
		}
	}
}
