package redact_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/core/security/redact"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestRedactorIsFrozenAtFiveMethods pins the published port's method set. A
// sixth method would break every downstream implementation and every test
// double at compile time (ADR 0039); a new capability is a sibling interface
// instead, and this test is what makes adding one to Redactor a decision
// rather than an edit.
func TestRedactorIsFrozenAtFiveMethods(t *testing.T) {
	t.Parallel()
	port := reflect.TypeFor[redact.Redactor]()
	got := make([]string, 0, port.NumMethod())
	for method := range port.Methods() {
		got = append(got, method.Name)
	}
	want := []string{"Attrs", "JSON", "Name", "Text", "Value"}
	//: reflect lists methods in lexical order, so the comparison is exact.
	if !slices.Equal(got, want) {
		t.Fatalf("Redactor methods = %v, want %v — widen by a sibling interface, never here", got, want)
	}
}

// TestTheSharedValuesFitTheBoundTheyDescribe pins the values every
// implementation writes and every caller compares against: the placeholder a
// secret becomes, and an ellipsis that is one character whose JSON spelling a
// cut at the floor can still hold.
func TestTheSharedValuesFitTheBoundTheyDescribe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ok   bool
	}{
		{"the placeholder is the SDK's spelling", redact.Placeholder == "[redacted]"},
		{"the marker of an unencodable value is not the placeholder", redact.Unencodable != redact.Placeholder},
		{"the ellipsis is one character", utf8.RuneCountInString(redact.Ellipsis) == 1},
		{"the floor holds the ellipsis as a JSON string", redact.MinBytes >= len(`"`+redact.Ellipsis+`"`)},
		{"the floor holds the placeholder as a JSON string", redact.MinBytes >= len(`"`+redact.Placeholder+`"`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !tc.ok {
				t.Fatalf("%s: does not hold (Placeholder %q, Ellipsis %q, MinBytes %d, Unencodable %q)",
					tc.name, redact.Placeholder, redact.Ellipsis, redact.MinBytes, redact.Unencodable)
			}
		})
	}
}

// TestTheRefusalsCarryTheirCodesAndNoInput pins the two sentinels to their
// codes — values allocated in the service layer and unchanged when their
// declarations moved here (ADR 0160) — and checks that neither Public says
// anything about what was refused beyond that it was refused.
func TestTheRefusalsCarryTheirCodesAndNoInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		sentinel *errs.Error
		code     errs.Code
		value    errs.Code
		reason   string
	}{
		{"a document that is not one JSON value", redact.DocumentInvalid, redact.CodeDocumentInvalid, 0x00_03_49_01, "DOCUMENT_INVALID"},
		{"a value encoding/json refuses", redact.ValueUnencodable, redact.CodeValueUnencodable, 0x00_03_49_02, "VALUE_UNENCODABLE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.code != tc.value {
				t.Fatalf("code = %v, want %v: a code keeps its value when its declaration moves", tc.code, tc.value)
			}
			if !errs.HasCode(tc.sentinel, tc.code) {
				t.Fatalf("%v does not carry %v", tc.sentinel, tc.code)
			}
			if got := tc.sentinel.Reason(); got != tc.reason {
				t.Errorf("Reason() = %q, want %q", got, tc.reason)
			}
			if public := tc.sentinel.Public(); !strings.Contains(public, "was not redacted") {
				t.Errorf("Public() = %q, want a sentence saying nothing was redacted", public)
			}
		})
	}
}
