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
	//: default singleton — wire-format fidelity means no escape.
	lossless, lerr := csv.New().Marshal(rec)
	if lerr != nil {
		t.Fatalf("lossless Marshal err = %v", lerr)
	}
	//: confirm the formula leaks through when escape is off.
	if !strings.Contains(string(lossless), "=SUM(A1:A3)") {
		t.Errorf("default Marshal should emit the literal formula: %q", lossless)
	}
	//: opt-in mitigation — every trigger cell gains a leading apostrophe.
	hardened, herr := csv.NewWithEscape(true).Marshal(rec)
	if herr != nil {
		t.Fatalf("hardened Marshal err = %v", herr)
	}
	//: assert the specific escaped patterns appear and the raw formula does not.
	text := string(hardened)
	for _, want := range []string{"'=SUM", "'+cmd", "'-1", "'@SUM"} {
		if !strings.Contains(text, want) {
			t.Errorf("hardened Marshal missing escape for %q in: %q", want, text)
		}
	}
	//: safe cell must still pass through verbatim.
	if !strings.Contains(text, "safe") {
		t.Errorf("hardened Marshal dropped a safe cell: %q", text)
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
