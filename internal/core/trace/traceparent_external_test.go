package trace_test

import (
	"errors"
	"strings"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
)

// The W3C specification's own example header, used verbatim so a reader can
// compare this file against §3.2 without translating anything.
const (
	specTraceID  = "4bf92f3577b34da6a3ce929d0e0e4736"
	specSpanID   = "00f067aa0ba902b7"
	specHeader   = "00-" + specTraceID + "-" + specSpanID + "-01"
	zeroTraceHex = "00000000000000000000000000000000"
	zeroSpanHex  = "0000000000000000"
)

// TestParseTraceParentAcceptsTheSpecificationExample is the positive case, and it
// asserts every field rather than only the error, so a parser that read the
// components in the wrong order would still fail here.
func TestParseTraceParentAcceptsTheSpecificationExample(t *testing.T) {
	context, err := coretrace.ParseTraceParent(specHeader)
	if err != nil {
		t.Fatalf("ParseTraceParent(%q): %v", specHeader, err)
	}
	if got := context.TraceID.String(); got != specTraceID {
		t.Errorf("trace-id = %q, want %q", got, specTraceID)
	}
	if got := context.SpanID.String(); got != specSpanID {
		t.Errorf("parent-id = %q, want %q", got, specSpanID)
	}
	if !context.IsSampled() {
		t.Error("the sampled bit was set in the header and is not set on the context")
	}
	if !context.Remote {
		t.Error("a context parsed from a header came from another process and must be Remote")
	}
}

// TestParseTraceParentRefusals walks the grammar's refusals one at a time.
//
// Each row names the SECTION that requires the refusal, because the cases that
// look arbitrary are exactly the ones a future reader would delete: an all-zero
// identifier is syntactically perfect hex, and uppercase hex decodes fine.
func TestParseTraceParentRefusals(t *testing.T) {
	cases := []struct {
		name   string
		header string
		why    string
	}{
		{"empty", "", "no header at all"},
		{"short", "00-" + specTraceID + "-" + specSpanID, "§3.2.4: shorter than 55 characters, do not parse"},
		{"version ff", "ff-" + specTraceID + "-" + specSpanID + "-01", "§3.2.2.1: version ff is invalid"},
		{"version not hex", "0g-" + specTraceID + "-" + specSpanID + "-01", "a version is 2HEXDIGLC"},
		{"all-zero trace-id", "00-" + zeroTraceHex + "-" + specSpanID + "-01", "§3.2.2.3: MUST ignore the traceparent"},
		{"all-zero parent-id", "00-" + specTraceID + "-" + zeroSpanHex + "-01", "§3.2.2.4: MUST ignore the traceparent"},
		{"uppercase trace-id", "00-" + strings.ToUpper(specTraceID) + "-" + specSpanID + "-01", "the grammar is 32HEXDIGLC, lowercase only"},
		{"uppercase flags", "00-" + specTraceID + "-" + specSpanID + "-0A", "trace-flags is 2HEXDIGLC"},
		{"non-hex flags", "00-" + specTraceID + "-" + specSpanID + "-zz", "trace-flags is 2HEXDIGLC"},
		{"trailing content on version 00", specHeader + "-cafe", "version 00 has no extension point"},
		{"underscore separator", "00_" + specTraceID + "-" + specSpanID + "-01", "the separator is a dash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			context, err := coretrace.ParseTraceParent(tc.header)
			if !errors.Is(err, coretrace.InvalidTraceParent) {
				t.Fatalf("want InvalidTraceParent (%s), got %v", tc.why, err)
			}
			if context.IsValid() {
				t.Error("a refused header must produce the invalid zero context")
			}
		})
	}
}

// TestParseTraceParentIsForwardCompatible pins §3.2.4, which is the section a
// version-00-only parser silently gets wrong.
//
// A future version keeps the first 55 characters exactly where 00 puts them, so a
// higher version MUST still parse — and anything past those 55 characters must
// begin with a dash. Without that last check a 56-character header whose 56th
// byte is a hex digit would be read as a valid context with a truncated flag
// field, which is a wrong sampled bit rather than a rejected header.
func TestParseTraceParentIsForwardCompatible(t *testing.T) {
	future := "cc-" + specTraceID + "-" + specSpanID + "-01"
	t.Run("higher version parses", func(t *testing.T) {
		context, err := coretrace.ParseTraceParent(future)
		if err != nil {
			t.Fatalf("a higher version must still be parsed: %v", err)
		}
		if !context.IsSampled() {
			t.Error("the sampled bit must be read from a higher version too")
		}
	})
	t.Run("higher version with a dashed tail parses", func(t *testing.T) {
		if _, err := coretrace.ParseTraceParent(future + "-beefcafe"); err != nil {
			t.Fatalf("unknown fields after a dash must be ignored, not refused: %v", err)
		}
	})
	t.Run("higher version with an undashed tail is refused", func(t *testing.T) {
		if _, err := coretrace.ParseTraceParent(future + "beefcafe"); !errors.Is(err, coretrace.InvalidTraceParent) {
			t.Fatalf("a tail that is not dash-delimited must be refused, got %v", err)
		}
	})
}

// TestFormatTraceParentMasksUndefinedFlagBits pins §3.2.2.5.2 — "vendors MUST set
// those to zero" — AND the decision not to apply it on the way in.
//
// Both halves matter. Clearing on receipt would destroy a bit a future version
// defines, which §3.2.4 forbids assuming anything about; emitting it would make
// this SDK a non-conforming producer. Keeping the byte and masking at format time
// is the only combination that satisfies both.
func TestFormatTraceParentMasksUndefinedFlagBits(t *testing.T) {
	header := "cc-" + specTraceID + "-" + specSpanID + "-ff"
	context, err := coretrace.ParseTraceParent(header)
	if err != nil {
		t.Fatalf("ParseTraceParent: %v", err)
	}
	if context.Flags != 0xFF {
		t.Errorf("received flags = %#x, want the byte kept verbatim (0xff)", context.Flags)
	}
	want := "00-" + specTraceID + "-" + specSpanID + "-01"
	got, ok := coretrace.FormatTraceParent(context)
	if !ok || got != want {
		t.Errorf("FormatTraceParent = %q (%v), want %q (undefined bits masked, version 00 emitted)", got, ok, want)
	}
}

// TestFormatTraceParentWritesNothingForAnInvalidContext pins the other half of the
// producer contract: an all-zero identifier is a header every conforming receiver
// is REQUIRED to ignore, so emitting one spends a header to say nothing and makes
// "we lost the context" indistinguishable from "we never had one".
func TestFormatTraceParentWritesNothingForAnInvalidContext(t *testing.T) {
	got, ok := coretrace.FormatTraceParent(coretrace.SpanContextValue{})
	if ok || got != "" {
		t.Errorf("FormatTraceParent(invalid) = %q (%v), want no header at all", got, ok)
	}
}

// TestFormatTraceParentRoundTripsItsOwnOutput checks the producer against the
// parser in the one direction that is safe: a header this SDK writes must be one
// this SDK — and therefore the grammar — accepts. It is not a substitute for the
// refusal table above, which is what actually pins the grammar.
func TestFormatTraceParentRoundTripsItsOwnOutput(t *testing.T) {
	original, err := coretrace.ParseTraceParent(specHeader)
	if err != nil {
		t.Fatalf("ParseTraceParent: %v", err)
	}
	rendered, ok := coretrace.FormatTraceParent(original)
	if !ok {
		t.Fatal("a valid context must render a header")
	}
	if len(rendered) != coretrace.TraceParentLen {
		t.Errorf("a version-00 traceparent is %d characters, got %d", coretrace.TraceParentLen, len(rendered))
	}
	reparsed, err := coretrace.ParseTraceParent(rendered)
	if err != nil {
		t.Fatalf("this SDK produced a header it cannot parse: %v", err)
	}
	if reparsed.TraceID != original.TraceID || reparsed.SpanID != original.SpanID {
		t.Error("the identifiers did not survive the round trip")
	}
}
