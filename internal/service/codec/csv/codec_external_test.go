package csv_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/csv"
)

// TestNew verifies the public constructor returns a non-nil singleton with
// the canonical name.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"canonical name", "csv"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := csv.New()
		if c == nil {
			t.Fatalf("%s: New returned nil", tc.name)
		}
		if got := c.Name(); got != tc.want {
			t.Errorf("%s: Name=%q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestMarshal covers matrix + pointer forms and the VALUE_INVALID error
// when a non-matrix input is passed.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr string
	}
	tests := []tc{
		{"matrix", [][]string{{"a", "b"}, {"c", "d"}}, ""},
		{"pointer to matrix", &[][]string{{"x"}}, ""},
		{"non-matrix input surfaces VALUE_INVALID", "nope", "VALUE_INVALID"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := csv.New().Marshal(tc.in)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Marshal err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestMarshal_FormulaEscape_OptIn asserts the OWASP CSV-injection
// mitigation (finding #19): the default singleton passes cells through
// verbatim, while NewWithEscape(true) prefixes any formula-trigger
// cell with a single quote so downstream spreadsheet apps render as text.
func TestMarshal_FormulaEscape_OptIn(t *testing.T) {
	t.Parallel()
	rec := [][]string{{"safe", "=SUM(A1:A3)", "+cmd|' /C calc'!A0", "-1", "@SUM", "normal"}}
	type tc struct {
		name     string
		codec    codec.Codec
		mustHave []string
		mustMiss []string
	}
	tests := []tc{
		{
			name:     "default codec leaks the formula verbatim",
			codec:    csv.New(),
			mustHave: []string{"=SUM(A1:A3)", "safe"},
		},
		{
			name:     "hardened codec escapes every trigger byte",
			codec:    csv.NewWithEscape(true),
			mustHave: []string{"'=SUM", "'+cmd", "'-1", "'@SUM", "safe"},
		},
		{
			name:     "explicit escape=false matches the default singleton",
			codec:    csv.NewWithEscape(false),
			mustHave: []string{"=SUM(A1:A3)", "safe"},
			mustMiss: []string{"'=SUM"},
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, err := tc.codec.Marshal(rec)
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		text := string(out)
		for _, want := range tc.mustHave {
			if !strings.Contains(text, want) {
				t.Errorf("%s: missing %q in %q", tc.name, want, text)
			}
		}
		for _, miss := range tc.mustMiss {
			if strings.Contains(text, miss) {
				t.Errorf("%s: unexpected %q in %q", tc.name, miss, text)
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestUnmarshal covers the decoder success path plus the UNMARSHAL_FAILED
// branch on inconsistent rows and VALUE_INVALID on wrong target type.
func TestUnmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    string
		target  any
		wantErr string
	}
	var matrix [][]string
	var wrong string
	tests := []tc{
		{"valid rows round-trip", "a,b\nc,d\n", &matrix, ""},
		{"inconsistent rows surface UNMARSHAL_FAILED", "a,b\nc\n", &matrix, "UNMARSHAL_FAILED"},
		{"wrong target surfaces VALUE_INVALID", "a", &wrong, "VALUE_INVALID"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := csv.New().Unmarshal([]byte(tc.data), tc.target)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Unmarshal err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNewWithEscape covers the public constructor that toggles the
// OWASP CSV-injection mitigation. Asserts both the escape=true and
// escape=false paths produce a non-nil codec with the canonical name
// and that the toggle flips the on-wire output for a trigger cell.
func TestNewWithEscape(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		escape    bool
		wantQuote bool
	}
	tests := []tc{
		{"escape=true escapes a trigger cell", true, true},
		{"escape=false leaves cells verbatim", false, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := csv.NewWithEscape(tc.escape)
		if c == nil {
			t.Fatalf("%s: NewWithEscape returned nil", tc.name)
		}
		if got := c.Name(); got != "csv" {
			t.Errorf("%s: Name=%q want %q", tc.name, got, "csv")
		}
		out, err := c.Marshal([][]string{{"=SUM"}})
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		hasQuote := len(out) > 0 && out[0] == '\''
		if hasQuote != tc.wantQuote {
			t.Errorf("%s: hasQuote=%v want %v (out=%q)", tc.name, hasQuote, tc.wantQuote, out)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRegisteredViaImport verifies the codec self-registers on package load.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func() bool
	}
	tests := []tc{
		{"format registered", func() bool { _, ok := codec.Lookup(codec.Format("csv")); return ok }},
		{"MIME resolved", func() bool { _, ok := codec.LookupMIME("text/csv"); return ok }},
		{"extension resolved", func() bool { _, ok := codec.LookupExt(".csv"); return ok }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if !tc.check() {
			t.Errorf("%s: lookup failed", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
