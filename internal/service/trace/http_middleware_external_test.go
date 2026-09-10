package trace_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctrace "github.com/kitsunium/sdk/internal/service/trace"
)

// TestServerMiddlewareIsTheNetDomainsOwnMiddlewareType pins the integration
// claim as a compile fact: the decorator IS corenet.Middleware[http.Handler], so
// it composes with corenet.Chain rather than beside it.
func TestServerMiddlewareIsTheNetDomainsOwnMiddlewareType(t *testing.T) {
	tracer := svctrace.NewTracer(svctrace.TracerConfig{})
	//: the declared types are the assertion: these ARE corenet.Middleware, so
	//: corenet.Chain composes them and no second middleware vocabulary exists.
	server := corenet.Middleware[http.Handler](svctrace.ServerMiddleware(tracer))
	client := corenet.Middleware[http.RoundTripper](svctrace.ClientMiddleware(tracer))
	if server(http.NotFoundHandler()) == nil || client(http.DefaultTransport) == nil {
		t.Fatal("both middlewares must decorate through the net domain's own middleware type")
	}
}

// TestServerMiddlewareJoinsTheUpstreamTrace is the inbound happy path: the
// traceparent is extracted, the span is a SERVER child of the remote parent, and
// the handler sees it on the context.
func TestServerMiddlewareJoinsTheUpstreamTrace(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
	var handlerSaw coretrace.SpanContextValue
	handler := svctrace.ServerMiddleware(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerSaw = coretrace.SpanContextFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/orders/42?q=x", nil)
	request.Header.Set(coretrace.TraceParentHeader, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	request.Header.Set(coretrace.TraceStateHeader, "congo=t61rcWkgMzE")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	spans := recorder.Collect().Spans
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	span := spans[0]
	if span.Kind != coretrace.SpanKindServer {
		t.Errorf("kind = %v, want SERVER", span.Kind)
	}
	if span.Context.TraceID.String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("the span left the upstream's trace: %s", span.Context.TraceID)
	}
	if !span.Parent.Remote {
		t.Error("the parent came from a header and must be marked Remote")
	}
	if value, ok := span.Context.State.Get("congo"); !ok || value != "t61rcWkgMzE" {
		t.Error("the vendor list must be inherited unchanged")
	}
	if handlerSaw.SpanID != span.Context.SpanID {
		t.Error("the handler did not see the span on its request context")
	}
}

// TestServerMiddlewareNamesTheSpanByMethodOnly pins the low-cardinality rule.
//
// A span name is what a backend GROUPS on. "GET /users/42" and "GET /users/43"
// are two operations to a backend and one to a human, so a service with a million
// users would have a million span names. The conventions ask for "{method}
// {route}" and this SDK does not route, so it uses the method and records the
// path as an ATTRIBUTE, where high cardinality is affordable.
func TestServerMiddlewareNamesTheSpanByMethodOnly(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
	handler := svctrace.ServerMiddleware(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/42", nil))

	span := recorder.Collect().Spans[0]
	if span.Name != http.MethodGet {
		t.Errorf("span name = %q, want %q — the path must not be in the name", span.Name, http.MethodGet)
	}
	if !hasAttr(span.Attrs, svctrace.URLPathKey, "/users/42") {
		t.Errorf("the path must be recorded as an attribute: %+v", span.Attrs)
	}
}

// TestServerMiddlewareRecordsTheEscapedPath pins the same reasoning
// corenet.RequestValue documents: url.URL.Path is already percent-DECODED, so
// recording it would report "%2e%2e" as ".." — a value the upstream may
// reinterpret and the trace would then attest to something that never happened.
func TestServerMiddlewareRecordsTheEscapedPath(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
	handler := svctrace.ServerMiddleware(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/a/%2e%2e/b", nil))

	span := recorder.Collect().Spans[0]
	if !hasAttr(span.Attrs, svctrace.URLPathKey, "/a/%2e%2e/b") {
		t.Errorf("the ESCAPED path must be recorded, got %+v", span.Attrs)
	}
}

// TestServerMiddlewareStatusAndErrorFloor pins the asymmetry a reader is most
// likely to assume is a slip: on a SERVER span 4xx is NOT an error. A 404 is the
// caller asking for something that is not there, which is the server working —
// marking it ERROR would make every scanner probing for /wp-admin light up a
// service's error rate.
func TestServerMiddlewareStatusAndErrorFloor(t *testing.T) {
	cases := []struct {
		status int
		want   coretrace.StatusCode
	}{
		{http.StatusOK, coretrace.StatusUnset},
		{http.StatusNotFound, coretrace.StatusUnset},
		{http.StatusTeapot, coretrace.StatusUnset},
		{http.StatusInternalServerError, coretrace.StatusError},
		{http.StatusBadGateway, coretrace.StatusError},
	}
	for _, tc := range cases {
		recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
		tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
		handler := svctrace.ServerMiddleware(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

		span := recorder.Collect().Spans[0]
		if span.Status.Code != tc.want {
			t.Errorf("status %d -> %v, want %v", tc.status, span.Status.Code, tc.want)
		}
		if !hasIntAttr(span.Attrs, svctrace.HTTPResponseStatusCodeKey, int64(tc.status)) {
			t.Errorf("status %d was not recorded as an attribute: %+v", tc.status, span.Attrs)
		}
	}
}

// TestServerMiddlewareRecordsTheImplicit200 pins the case a naive recorder gets
// wrong: a handler that returns having written nothing still produced a 200,
// because net/http writes one.
func TestServerMiddlewareRecordsTheImplicit200(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
	handler := svctrace.ServerMiddleware(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !hasIntAttr(recorder.Collect().Spans[0].Attrs, svctrace.HTTPResponseStatusCodeKey, http.StatusOK) {
		t.Error("a handler that wrote nothing still produced a 200")
	}
}

// TestServerMiddlewarePreservesWriterCapabilities is the trap this middleware was
// written to avoid, and it is the same defect class ADR 0047 fixed in the listener
// engine.
//
// A wrapper that declared Flush and Hijack itself would claim BOTH capabilities
// unconditionally, so `w.(http.Hijacker)` would succeed on a writer that cannot
// hijack — and the failure would surface inside a protocol upgrade rather than as
// a clean "not supported". Cooperating with http.ResponseController's Unwrap chain
// is what keeps the answer truthful in both directions.
func TestServerMiddlewarePreservesWriterCapabilities(t *testing.T) {
	tracer := svctrace.NewTracer(svctrace.TracerConfig{})
	t.Run("a flushable writer stays flushable", func(t *testing.T) {
		var flushed bool
		handler := svctrace.ServerMiddleware(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("Flush through the wrapper: %v", err)
				return
			}
			flushed = true
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		if !flushed {
			t.Error("the wrapper hid the underlying writer's Flush")
		}
	})
	t.Run("a non-hijackable writer stays non-hijackable", func(t *testing.T) {
		handler := svctrace.ServerMiddleware(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _, err := http.NewResponseController(w).Hijack()
			if !errors.Is(err, http.ErrNotSupported) {
				t.Errorf("Hijack error = %v, want ErrNotSupported — the wrapper must not CLAIM the capability", err)
			}
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
}

// TestServerMiddlewareNeverFailsARequestOverABadHeader pins §4.3 at the edge that
// matters: the header is written by a stranger, so rejecting a request over it is
// a denial of service with extra steps.
func TestServerMiddlewareNeverFailsARequestOverABadHeader(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
	var served bool
	handler := svctrace.ServerMiddleware(tracer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(coretrace.TraceParentHeader, "00-00000000000000000000000000000000-00f067aa0ba902b7-01")
	request.Header.Set(coretrace.TraceStateHeader, "congo=t61rcWkgMzE")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if !served || response.Code != http.StatusOK {
		t.Fatalf("the request was not served normally: served=%v code=%d", served, response.Code)
	}
	span := recorder.Collect().Spans[0]
	if !span.IsRoot() {
		t.Error("a malformed traceparent must start a NEW trace, not join a broken one")
	}
	if span.Context.State.Len() != 0 {
		t.Error("§4.3 deletes tracestate along with a malformed parent")
	}
}

// TestClientMiddlewareInjectsAndClonesTheRequest pins both halves of the outbound
// contract: the header goes on the wire, and the caller's own *http.Request is
// left untouched — http.RoundTripper's contract says an implementation "should
// not modify the request", and net/http reuses the same value across retries.
func TestClientMiddlewareInjectsAndClonesTheRequest(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
	var sentHeader string
	transport := svctrace.ClientMiddleware(tracer)(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		sentHeader = r.Header.Get(coretrace.TraceParentHeader)
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))

	request := httptest.NewRequest(http.MethodPost, "https://api.example/v1/charge?token=x", nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	closeBody(t, response)

	if sentHeader == "" {
		t.Fatal("no traceparent was injected into the outbound request")
	}
	if request.Header.Get(coretrace.TraceParentHeader) != "" {
		t.Error("the caller's own request was mutated; RoundTrip must clone")
	}
	span := recorder.Collect().Spans[0]
	if span.Kind != coretrace.SpanKindClient {
		t.Errorf("kind = %v, want CLIENT", span.Kind)
	}
	if rendered, ok := coretrace.FormatTraceParent(span.Context); !ok || rendered != sentHeader {
		t.Errorf("the injected header %q does not name the span that was recorded", sentHeader)
	}
	if !hasAttr(span.Attrs, svctrace.URLFullKey, "https://api.example/v1/charge?token=x") {
		t.Errorf("the outbound URL was not recorded: %+v", span.Attrs)
	}
}

// TestClientMiddlewareInjectsAnUnsampledDecisionToo pins the reason an unsampled
// span is a noop with a CONTEXT: without the header the upstream would start its
// own root and one dropped trace would become N kept ones.
func TestClientMiddlewareInjectsAnUnsampledDecisionToo(t *testing.T) {
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sampler: svctrace.NeverSample})
	var sentHeader string
	transport := svctrace.ClientMiddleware(tracer)(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		sentHeader = r.Header.Get(coretrace.TraceParentHeader)
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))
	response, err := transport.RoundTrip(httptest.NewRequest(http.MethodGet, "https://api.example/v1/ping", nil))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	closeBody(t, response)
	if sentHeader == "" {
		t.Fatal("an unsampled trace must still propagate its decision")
	}
	if sentHeader[len(sentHeader)-2:] != "00" {
		t.Errorf("traceparent = %q, want flags saying not-sampled", sentHeader)
	}
}

// TestClientMiddlewareErrorFloor pins the OTHER side of the asymmetry: on a
// CLIENT span a 4xx IS an error, because the caller asked for something and did
// not get it.
func TestClientMiddlewareErrorFloor(t *testing.T) {
	cases := []struct {
		status int
		want   coretrace.StatusCode
	}{
		{http.StatusOK, coretrace.StatusUnset},
		{http.StatusNoContent, coretrace.StatusUnset},
		{http.StatusNotFound, coretrace.StatusError},
		{http.StatusInternalServerError, coretrace.StatusError},
	}
	for _, tc := range cases {
		recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
		tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
		transport := svctrace.ClientMiddleware(tracer)(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, Body: http.NoBody}, nil
		}))
		response, err := transport.RoundTrip(httptest.NewRequest(http.MethodGet, "https://api.example/v1/x", nil))
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		closeBody(t, response)
		if got := recorder.Collect().Spans[0].Status.Code; got != tc.want {
			t.Errorf("status %d -> %v, want %v", tc.status, got, tc.want)
		}
	}
}

// TestClientMiddlewareRecordsATransportFault pins that a call with no response
// still closes its span, and that the upstream's error text — which may name an
// internal address — is not copied onto the span.
func TestClientMiddlewareRecordsATransportFault(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
	fault := errors.New("dial tcp 10.0.0.7:443: connection refused")
	transport := svctrace.ClientMiddleware(tracer)(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fault
	}))
	_, err := transport.RoundTrip(httptest.NewRequest(http.MethodGet, "https://api.example/v1/x", nil))
	if !errors.Is(err, fault) {
		t.Fatalf("the middleware reshaped the transport fault: %v", err)
	}
	span := recorder.Collect().Spans[0]
	if span.Status.Code != coretrace.StatusError {
		t.Errorf("status = %v, want ERROR", span.Status.Code)
	}
	if span.Status.Message != "" {
		t.Errorf("status message = %q, want empty — a free-text field is read verbatim by an operator", span.Status.Message)
	}
	if !hasAttr(span.Attrs, svctrace.ErrorTypeKey, "transport") {
		t.Errorf("error.type was not recorded: %+v", span.Attrs)
	}
}

// TestRecordErrorWritesTheConventionalExceptionEvent pins the helper, including
// the choice of the dotted-quad CODE as exception.type: a code is the stable
// identity of a failure across a trace, a log line and an alert, while a Go type
// name is something nobody outside the process can act on.
func TestRecordErrorWritesTheConventionalExceptionEvent(t *testing.T) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{})
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sink: recorder.Sink()})
	_, span := tracer.Start(t.Context(), "op", coretrace.SpanParams{})
	svctrace.RecordError(span, svctrace.OTLPPartialSuccess)
	span.End()

	recorded := recorder.Collect().Spans[0]
	if len(recorded.Events) != 1 || recorded.Events[0].Name != coretrace.ExceptionEventName {
		t.Fatalf("events = %+v, want one %q event", recorded.Events, coretrace.ExceptionEventName)
	}
	attrs := recorded.Events[0].Attrs
	code, ok := errs.CodeOf(svctrace.OTLPPartialSuccess)
	if !ok {
		t.Fatal("the sentinel must carry a dotted-quad code")
	}
	if !hasAttr(attrs, coretrace.ExceptionTypeKey, code.String()) {
		t.Errorf("exception.type = %+v, want the dotted-quad %q", attrs, code)
	}
	if recorded.Status.Code != coretrace.StatusError {
		t.Error("RecordError must also mark the span ERROR")
	}
	//: a nil span and a nil error are no-ops rather than panics.
	svctrace.RecordError(nil, svctrace.OTLPPartialSuccess)
	svctrace.RecordError(span, nil)
}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// hasAttr reports whether attrs carries key with the given string value.
func hasAttr(attrs []coremetrics.AttrValue, key, value string) bool {
	for _, attr := range attrs {
		if attr.Key == key && attr.Str() == value {
			return true
		}
	}
	return false
}

// hasIntAttr reports whether attrs carries key with the given integer value.
func hasIntAttr(attrs []coremetrics.AttrValue, key string, value int64) bool {
	for _, attr := range attrs {
		if attr.Key == key && attr.Int64() == value {
			return true
		}
	}
	return false
}
