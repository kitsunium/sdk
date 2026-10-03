package trace_test

import (
	"net/http"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
)

// headerIsACarrier is the ADR 0039 guard for the Carrier port, expressed as a
// line of code rather than a comment: http.Header's Get/Set pair is EXACTLY the
// interface, so a third method stops this compiling — and with it every
// `Inject(ctx, req.Header)` call site in the SDK (the engine's Inject, in
// internal/service/observe/trace, where the propagation tests live).
var headerIsACarrier coretrace.Carrier = http.Header{}

// TestHTTPHeaderIsACarrierWithNoAdapter exercises the guard above through the
// interface, so the declaration is not merely compiled but used.
func TestHTTPHeaderIsACarrierWithNoAdapter(t *testing.T) {
	headerIsACarrier.Set("traceparent", specHeader)
	if got := headerIsACarrier.Get("traceparent"); got != specHeader {
		t.Fatalf("http.Header must satisfy Carrier with no adapter, got %q", got)
	}
}
