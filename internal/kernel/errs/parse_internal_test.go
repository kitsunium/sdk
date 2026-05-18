package errs

import (
	"strings"
	"testing"
)

func Test_parseOctet(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		seg  string
		want uint8
		ok   bool
	}
	tests := []tc{
		{"zero", "0", 0, true},
		{"single digit", "7", 7, true},
		{"two digits", "42", 42, true},
		{"three digits", "255", 255, true},
		{"empty", "", 0, false},
		{"too long", "1234", 0, false},
		{"leading zero double", "01", 0, false},
		{"leading zero triple", "001", 0, false},
		{"non digit", "1a2", 0, false},
		{"only letters", "abc", 0, false},
		{"overflow 256", "256", 0, false},
		{"overflow 999", "999", 0, false},
		{"sign minus", "-1", 0, false},
		{"sign plus", "+1", 0, false},
		{"space inside", "1 2", 0, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := parseOctet(c.seg)
		if ok != c.ok {
			//: ok bit drift breaks parseOctet's contract with ParseCode.
			t.Fatalf("parseOctet(%q) ok=%v, want %v", c.seg, ok, c.ok)
		}
		if ok && got != c.want {
			//: value drift would silently corrupt parsed Codes.
			t.Fatalf("parseOctet(%q) = %d, want %d", c.seg, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_digitsToOctet(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		seg  string
		want uint8
		ok   bool
	}
	tests := []tc{
		{"zero", "0", 0, true},
		{"max", "255", 255, true},
		{"overflow 256", "256", 0, false},
		{"non digit byte", "1!2", 0, false},
		{"alpha", "12a", 0, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := digitsToOctet(c.seg)
		if ok != c.ok {
			//: ok bit drift propagates to parseOctet's caller logic.
			t.Fatalf("digitsToOctet(%q) ok=%v, want %v", c.seg, ok, c.ok)
		}
		if ok && got != c.want {
			//: numeric drift means accumulator math is wrong.
			t.Fatalf("digitsToOctet(%q) = %d, want %d", c.seg, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_scanOctets(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      string
		want    [codeSegmentCount]uint8
		wantErr bool
	}
	tests := []tc{
		{"canonical", "1.2.3.4", [codeSegmentCount]uint8{1, 2, 3, 4}, false},
		{"max", "255.255.255.255", [codeSegmentCount]uint8{255, 255, 255, 255}, false},
		{"too many segments", "1.2.3.4.5", [codeSegmentCount]uint8{}, true},
		{"too few segments", "1.2.3", [codeSegmentCount]uint8{}, true},
		{"bad segment", "1.2.3.abc", [codeSegmentCount]uint8{}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := scanOctets(c.in)
		if (err != nil) != c.wantErr {
			//: err presence/absence drift breaks ParseCode's branch logic.
			t.Fatalf("scanOctets(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			//: octet array mismatch corrupts the eventual Pack call.
			t.Fatalf("scanOctets(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_parseFailure(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", inputEchoMax*2)
	type tc struct {
		name       string
		input      string
		detail     string
		wantInPriv string
		truncated  bool
	}
	tests := []tc{
		{"short input echoed verbatim", "bad", "length outside", "ParseCode(bad): length outside", false},
		{"detail appended", "1.2.3", "expected 4 segments", "expected 4 segments", false},
		{"long input truncated", long, "len", "...", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := parseFailure(c.input, c.detail)
		if got == nil {
			//: nil would break the typed-errors-only invariant; halt subtest.
			t.Fatal("parseFailure returned nil")
			return
		}
		if got.code != CodeInvalidCodeString {
			//: the failure code is the public contract for ParseCode failures.
			t.Fatalf("code = %d, want %d", uint32(got.code), CodeInvalidCodeString)
		}
		if got.reason != "INVALID_CODE_STRING" {
			//: reason drift breaks downstream telemetry filters.
			t.Fatalf("reason = %q, want INVALID_CODE_STRING", got.reason)
		}
		if got.public != "invalid code string" {
			//: public message is wire-stable; drift is a contract break.
			t.Fatalf("public = %q", got.public)
		}
		if !strings.Contains(got.private, c.wantInPriv) {
			//: private must carry the diagnostic detail for log triage.
			t.Fatalf("private = %q, want substring %q", got.private, c.wantInPriv)
		}
		if c.truncated && !strings.Contains(got.private, "...") {
			//: truncation marker must appear when input exceeds inputEchoMax.
			t.Fatalf("expected truncation marker in private: %q", got.private)
		}
		if !c.truncated && strings.Contains(got.private, "x...") {
			//: short inputs must not be truncated.
			t.Fatalf("unexpected truncation in private: %q", got.private)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
