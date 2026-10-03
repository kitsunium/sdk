package trace_test

import (
	"net/http"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/observe/trace"
)

// TestInjectExtractRoundTrip is the happy path across a process boundary.
func TestInjectExtractRoundTrip(t *testing.T) {
	state, err := svctrace.ParseTraceState("congo=t61rcWkgMzE")
	if err != nil {
		t.Fatalf("ParseTraceState: %v", err)
	}
	original, err := svctrace.ParseTraceParent(specHeader)
	if err != nil {
		t.Fatalf("ParseTraceParent: %v", err)
	}
	header := http.Header{}
	svctrace.Inject(original.WithState(state), header)
	if got := header.Get(svctrace.TraceParentHeader); got != specHeader {
		t.Errorf("traceparent = %q, want %q", got, specHeader)
	}
	if got, want := header.Get(svctrace.TraceStateHeader), "congo=t61rcWkgMzE"; got != want {
		t.Errorf("tracestate = %q, want %q", got, want)
	}
	extracted := svctrace.Extract(header)
	if extracted.TraceID != original.TraceID || extracted.SpanID != original.SpanID {
		t.Error("the identifiers did not survive Inject/Extract")
	}
	if value, ok := extracted.State.Get("congo"); !ok || value != "t61rcWkgMzE" {
		t.Errorf("the vendor list did not survive: %q, %v", value, ok)
	}
}

// TestInjectWritesNothingForAnInvalidContext pins that an unusable context spends
// no header. Emitting an all-zero identifier would produce a traceparent every
// conforming receiver is REQUIRED to ignore (§3.2.2.3/§3.2.2.4), which makes "we
// lost the context here" indistinguishable from "we never had one".
func TestInjectWritesNothingForAnInvalidContext(t *testing.T) {
	header := http.Header{}
	svctrace.Inject(coretrace.SpanContextValue{}, header)
	if len(header) != 0 {
		t.Errorf("an invalid context wrote %v; it must write nothing", header)
	}
}

// TestInjectOmitsAnEmptyTraceState pins §4.2 from the producing side: a
// tracestate without a traceparent "is invalid and MUST be discarded", so the two
// are a pair and a blank one is not half of it.
func TestInjectOmitsAnEmptyTraceState(t *testing.T) {
	context, err := svctrace.ParseTraceParent(specHeader)
	if err != nil {
		t.Fatalf("ParseTraceParent: %v", err)
	}
	header := http.Header{}
	svctrace.Inject(context, header)
	if _, present := header[http.CanonicalHeaderKey(svctrace.TraceStateHeader)]; present {
		t.Error("an empty vendor list must be omitted, not written blank")
	}
}

// TestExtractRestartsTheTraceOnAMalformedParent pins §4.3: "the vendor creates a
// new traceparent header and deletes tracestate".
//
// It also pins the shape of the API. Extract returns no error, because the
// specification prescribes exactly one response and the header is written by a
// stranger — handing a caller an error would invite them to fail the request,
// which is a denial of service with extra steps.
func TestExtractRestartsTheTraceOnAMalformedParent(t *testing.T) {
	header := http.Header{}
	header.Set(svctrace.TraceParentHeader, "00-"+zeroTraceHex+"-"+specSpanID+"-01")
	header.Set(svctrace.TraceStateHeader, "congo=t61rcWkgMzE")
	extracted := svctrace.Extract(header)
	if extracted.IsValid() {
		t.Error("a malformed traceparent must yield the invalid zero context")
	}
	if extracted.State.Len() != 0 {
		t.Error("§4.3 deletes tracestate with the parent: keeping it would attach another system's state to a brand-new trace")
	}
}

// TestExtractKeepsAValidParentDespiteAnUnreadableTraceState pins the OTHER half
// of §4.3, which is the one a symmetric implementation gets wrong: validating
// tracestate is a MAY and discarding just that header is permitted, while the
// parent is independently well-formed. Losing a valid parent over an unreadable
// vendor list would break the trace to protect an annotation.
func TestExtractKeepsAValidParentDespiteAnUnreadableTraceState(t *testing.T) {
	header := http.Header{}
	header.Set(svctrace.TraceParentHeader, specHeader)
	header.Set(svctrace.TraceStateHeader, "a=1,a=2")
	extracted := svctrace.Extract(header)
	if !extracted.IsValid() {
		t.Fatal("a valid traceparent must survive an unreadable tracestate")
	}
	if extracted.State.Len() != 0 {
		t.Error("the unreadable vendor list must be discarded, not salvaged")
	}
}

// TestExtractRestartsOnTwoTraceParentHeaders pins the outcome of the RFC's rule
// on repeated header fields: two upstreams claiming different parents merge into
// "v1,v2", which fails the grammar and restarts the trace. That is the answer — neither
// claim may be believed — and it falls out of the grammar rather than needing a
// rule of its own.
func TestExtractRestartsOnTwoTraceParentHeaders(t *testing.T) {
	header := http.Header{}
	header.Add(svctrace.TraceParentHeader, specHeader)
	header.Add(svctrace.TraceParentHeader, "00-"+specTraceID+"-0102030405060708-00")
	merged := http.Header{}
	merged.Set(svctrace.TraceParentHeader, header.Get(svctrace.TraceParentHeader)+","+header.Values(svctrace.TraceParentHeader)[1])
	if svctrace.Extract(merged).IsValid() {
		t.Error("two merged traceparent values must fail the grammar and restart the trace")
	}
}

// TestExtractOnAnEmptyCarrier pins the ordinary case: most requests carry no
// trace at all, and that is not a fault.
func TestExtractOnAnEmptyCarrier(t *testing.T) {
	if svctrace.Extract(http.Header{}).IsValid() {
		t.Error("an empty carrier must yield the invalid zero context")
	}
}
