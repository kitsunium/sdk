package errs_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestParseCode_ValidCanonical(t *testing.T) {
	t.Parallel()
	type tc struct {
		in   string
		want errs.Code
	}
	tests := []tc{
		{"0.0.0.0", 0x00_00_00_00},
		{"0.0.0.1", 0x00_00_00_01},
		{"1.2.3.4", 0x01_02_03_04},
		{"255.255.255.255", 0xFF_FF_FF_FF},
		{"100.200.50.5", 0x64_C8_32_05},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := errs.ParseCode(c.in)
		if err != nil {
			t.Fatalf("ParseCode(%q) unexpected error: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ParseCode(%q) = %#08x, want %#08x", c.in, uint32(got), uint32(c.want))
		}
		//: round-trip via String().
		if got.String() != c.in {
			t.Fatalf("round-trip failed: String() = %q, want %q", got.String(), c.in)
		}
	}
	for _, c := range tests {
		c := c
		t.Run(c.in, func(t *testing.T) { t.Parallel(); runCase(t, c) })
	}
}

func TestParseCode_RejectsBadInputs(t *testing.T) {
	t.Parallel()
	bad := []string{
		"",                         // empty
		"1.1.1",                    // too few segments
		"1.1.1.1.1",                // too many segments
		"256.0.0.0",                // octet overflow
		"1.256.0.0",
		"1.1.1.256",
		"0001.0.0.0",               // segment too long
		"01.1.1.1",                 // leading zero (padded form)
		"001.001.001.001",          // full padded form
		" 1.1.1.1",                 // leading whitespace
		"1.1.1.1 ",                 // trailing whitespace
		"-1.0.0.0",                 // negative
		"+1.0.0.0",                 // plus sign
		"1..1.1",                   // empty segment
		"a.b.c.d",                  // non-digit
		"1.1.1.1a",                 // trailing junk
	}
	for _, s := range bad {
		s := s
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			_, err := errs.ParseCode(s)
			if err == nil {
				t.Fatalf("ParseCode(%q) expected error, got nil", s)
			}
		})
	}
}

func TestParseCode_ReturnsTypedError(t *testing.T) {
	t.Parallel()
	_, err := errs.ParseCode("bogus")
	if err == nil {
		t.Fatal("expected error")
	}
	//: must be *errs.Error per the typed-errors-only rule.
	var typed *errs.Error
	if !errors.As(err, &typed) {
		t.Fatalf("expected *errs.Error, got %T", err)
	}
	if typed.Code() != errs.CodeInvalidCodeString {
		t.Fatalf("expected Code == %d (CodeInvalidCodeString), got %d",
			errs.CodeInvalidCodeString, typed.Code())
	}
	if typed.Reason() != "INVALID_CODE_STRING" {
		t.Fatalf("unexpected Reason: %q", typed.Reason())
	}
}
