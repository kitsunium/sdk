// Package net — the shared sentinel-wrapping helper.
package net

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_wrapAs pins the origin-wins decision every fallible path in this package
// depends on: the SENTINEL is the origin, so its code, reason and public
// message survive, while the cause rides along as a field. Wrapping the cause
// instead would let an *errs.Error from a dependency hijack the domain code, and
// callers matching on it would silently stop matching.
func Test_wrapAs(t *testing.T) {
	t.Parallel()
	//: a cause that is itself typed is the case origin-wins exists for.
	typedCause := errs.Define(errs.Code(0x00_02_0F_01), "FOREIGN_REASON",
		"a foreign public message", "a foreign private message")

	type tc struct {
		name         string
		cause        error
		fields       []errs.FieldValue
		wantHasCause bool
	}
	tests := []tc{
		{"a nil cause", nil, nil, false},
		{"a nil cause with fields", nil, []errs.FieldValue{errs.String("addr", "tcp://:0")}, false},
		{"a plain cause", errors.New("dial refused"), nil, true},
		{"a typed cause", typedCause, nil, true},
		{
			"a typed cause beside fields",
			typedCause,
			[]errs.FieldValue{errs.String("addr", "tcp://:0")},
			true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := wrapAs(ListenFailed, c.cause, c.fields...)
		if got == nil {
			t.Fatal("wrapAs returned nil, want the wrapped sentinel")
		}

		//: the sentinel is the origin: its code and reason must survive even a
		//: typed cause carrying its own.
		wantCode, _ := errs.CodeOf(ListenFailed)
		if !errs.HasCode(got, wantCode) {
			t.Errorf("wrapAs = %v, want code %v", got, wantCode)
		}
		wantReason, _ := errs.ReasonOf(ListenFailed)
		if reason, _ := errs.ReasonOf(got); reason != wantReason {
			t.Errorf("reason = %q, want %q", reason, wantReason)
		}
		//: the caller must be able to match the sentinel, which is the whole
		//: point of returning it rather than the cause.
		if !errors.Is(got, ListenFailed) {
			t.Errorf("errors.Is(err, ListenFailed) = false for %v", got)
		}

		fields := errs.FieldsOf(got)
		//: a nil cause contributes no field; anything else rides as one, so the
		//: original message stays diagnosable.
		hasCause := false
		for _, f := range fields {
			if f.Key() == "cause" {
				hasCause = true
			}
		}
		if hasCause != c.wantHasCause {
			t.Errorf("a cause field is present = %v, want %v (fields %v)", hasCause, c.wantHasCause, fields)
		}
		//: the caller's own fields are never dropped to make room for it.
		if len(fields) < len(c.fields) {
			t.Errorf("fields = %v, want at least the %d supplied", fields, len(c.fields))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
