package errs_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestParseCode(t *testing.T) {
	t.Parallel()
	type tc struct {
		in   string
		want errs.Code
	}
	tests := []tc{
		{"0.0.0.1", 0x00_00_00_01},
		{"1.2.3.4", 0x01_02_03_04},
		{"255.255.255.255", 0xFF_FF_FF_FF},
		{"100.200.50.5", 0x64_C8_32_05},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := errs.ParseCode(c.in)
		if err != nil {
			//: any error on a canonical input is a regression.
			t.Fatalf("ParseCode(%q) unexpected error: %v", c.in, err)
		}
		if got != c.want {
			//: numeric mismatch — Pack composition broke.
			t.Fatalf("ParseCode(%q) = %#08x, want %#08x", c.in, uint32(got), uint32(c.want))
		}
		//: round-trip via String() — canonical form must be stable.
		if got.String() != c.in {
			t.Fatalf("round-trip failed: String() = %q, want %q", got.String(), c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestParseCode_RejectsBadInputs(t *testing.T) {
	t.Parallel()
	type tc struct {
		in string
	}
	tests := []tc{
		{""},                // empty
		{"1.1.1"},           // too few segments
		{"1.1.1.1.1"},       // too many segments
		{"256.0.0.0"},       // octet overflow
		{"1.256.0.0"},       //
		{"1.1.1.256"},       //
		{"0001.0.0.0"},      // segment too long
		{"01.1.1.1"},        // leading zero (padded form)
		{"001.001.001.001"}, // full padded form
		{" 1.1.1.1"},        // leading whitespace
		{"1.1.1.1 "},        // trailing whitespace
		{"-1.0.0.0"},        // negative
		{"+1.0.0.0"},        // plus sign
		{"1..1.1"},          // empty segment
		{"a.b.c.d"},         // non-digit
		{"1.1.1.1a"},        // trailing junk
		{"0.0.0.0"},         // reserved zero sentinel — rejected by ADR 0005 rule
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := errs.ParseCode(c.in)
		if err == nil {
			//: every entry above MUST be rejected; nil means the rule slipped.
			t.Fatalf("ParseCode(%q) expected error, got nil", c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestParseCode_ReturnsTypedError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		in         string
		wantCode   errs.Code
		wantReason string
	}
	tests := []tc{
		{"non-digit input", "bogus", errs.CodeInvalidCodeString, "INVALID_CODE_STRING"},
		{"too short", "1.1.1", errs.CodeInvalidCodeString, "INVALID_CODE_STRING"},
		{"padded form", "001.001.001.001", errs.CodeInvalidCodeString, "INVALID_CODE_STRING"},
		{"zero sentinel", "0.0.0.0", errs.CodeInvalidCodeString, "INVALID_CODE_STRING"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := errs.ParseCode(c.in)
		if err == nil {
			//: typed-error contract requires a non-nil failure for these inputs.
			t.Fatal("expected error")
		}
		var typed *errs.Error
		//: must be *errs.Error per the typed-errors-only rule.
		if !errors.As(err, &typed) {
			t.Fatalf("expected *errs.Error, got %T", err)
		}
		if typed.Code() != c.wantCode {
			//: code drift breaks every callers' sentinel match.
			t.Fatalf("expected Code == %#08x, got %#08x", uint32(c.wantCode), uint32(typed.Code()))
		}
		if typed.Reason() != c.wantReason {
			//: reason drift breaks log/telemetry filters.
			t.Fatalf("unexpected Reason: %q", typed.Reason())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
