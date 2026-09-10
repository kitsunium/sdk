package trace_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/trace"
)

// The two inbound traceparents the middleware is measured against. Both are
// syntactically valid W3C Trace Context version-00 headers over the SAME trace
// and parent span; only the flag byte differs, so the pair isolates the sampled
// bit and nothing else.
//
// A request carrying one of these takes a DIFFERENT path through Start than a
// request carrying none: the trace id is inherited rather than minted (one
// fewer CSPRNG read) and the sampler is never consulted, because the decision
// was taken at the root. That is the shape of every request but the first in a
// traced deployment, which is why it is measured separately from the root case.
const (
	sampledTraceParent   string = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	unsampledTraceParent string = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
)

// benchStatus is the status the benchmark handler writes. It is below
// httpServerErrorFloor on purpose: a 5xx would add a SetStatus call to the
// traced arm and to no other, which would price an error path as if it were the
// ordinary one.
const benchStatus int = http.StatusOK

// Typed sinks. A single `var sink any` would BOX every value stored into it and
// charge the allocation to the code under test — the error that once published
// "1 alloc" for a function documented at zero, earlier in this campaign.
var (
	handlerSink http.Handler
	statusSink  int
)

// benchResponseWriter is a http.ResponseWriter that allocates nothing per call.
//
// httptest.NewRecorder allocates a bytes.Buffer, a header map and a
// ResponseRecorder per request, which would appear in every arm of this file —
// including the baseline — and would be several times the delta being measured.
// The header map here is built once and handed out by reference.
type benchResponseWriter struct {
	// header is built once at construction and never replaced.
	header http.Header
	// status is the last code written, kept only so the loop body has an
	// observable effect the compiler cannot reason away.
	status int
}

// Header returns the one map, never a fresh one.
func (w *benchResponseWriter) Header() http.Header {
	//: the same map every call, so no arm is charged for building one.
	return w.header
}

// Write discards, reporting a full write.
func (w *benchResponseWriter) Write(data []byte) (n int, err error) {
	//: nothing is retained; the byte count keeps io.Writer's contract.
	return len(data), nil
}

// WriteHeader remembers the code and discards it.
func (w *benchResponseWriter) WriteHeader(status int) {
	//: a plain field store, paid identically by every arm.
	w.status = status
}

// newBenchResponseWriter builds the shared writer.
func newBenchResponseWriter() *benchResponseWriter {
	//: one header map for the whole run.
	return &benchResponseWriter{header: make(http.Header, 4)}
}

// benchHandler is the handler EVERY arm below runs, traced or not. It writes a
// status and nothing else: the comparison is only honest while both sides
// execute the identical body, so the delta is the middleware and not the work.
var benchHandler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	//: an explicit status, so the middleware's statusRecorder.WriteHeader runs
	//: on the traced arm exactly as it would under a real handler.
	w.WriteHeader(benchStatus)
})

// benchRequest builds the inbound request once, with the given traceparent or
// none. It is reused across iterations: r.WithContext copies it, which is the
// middleware's cost and is charged to the middleware.
func benchRequest(b *testing.B, traceParent string) *http.Request {
	b.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/orders/42?page=2", nil)
	request.Host = "api.example.com"
	if traceParent != "" {
		request.Header.Set("traceparent", traceParent)
	}
	return request
}

// benchTracedHandler wires ServerMiddleware around benchHandler with a sink
// that discards, so the number is the middleware's own cost with export held at
// zero. Recorder.record is priced separately, in recorder_bench_test.go.
func benchTracedHandler(b *testing.B, sampler coretrace.Sampler) http.Handler {
	b.Helper()
	tracer := svctrace.NewTracer(svctrace.TracerConfig{
		Sampler: sampler,
		Sink:    func(coretrace.SpanValue) {},
	})
	//: the type is corenet.Middleware[http.Handler]; that it is, is pinned by
	//: TestServerMiddlewareIsTheNetDomainsOwnMiddlewareType rather than by a
	//: declaration here, so this file measures and asserts nothing.
	middleware := svctrace.ServerMiddleware(tracer)
	return middleware(benchHandler)
}

// serveBench runs handler against request for the whole timed loop. Every arm
// goes through it, so the loop, the writer and the sink stores are identical
// across arms and cancel out of every delta.
func serveBench(b *testing.B, handler http.Handler, request *http.Request) {
	b.Helper()
	writer := newBenchResponseWriter()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		handler.ServeHTTP(writer, request)
	}
	b.StopTimer()
	//: typed sinks, so nothing here is boxed into an `any` and charged to the
	//: code under test.
	handlerSink, statusSink = handler, writer.status
}

// BenchmarkServeHTTP_Untraced is the CONTROL every other row in this file is
// read against: the same handler, the same writer, the same request, reached
// through the same http.Handler interface call, with no middleware at all.
//
// Its allocation count is the number that has to be subtracted from each traced
// row to get what tracing ADDS to a request.
func BenchmarkServeHTTP_Untraced(b *testing.B) {
	serveBench(b, benchHandler, benchRequest(b, ""))
}

// BenchmarkServeHTTP_TracedRootSampled is the most expensive shape: no inbound
// traceparent, so the middleware mints a trace id AND a span id, consults the
// sampler, and records.
func BenchmarkServeHTTP_TracedRootSampled(b *testing.B) {
	serveBench(b, benchTracedHandler(b, svctrace.AlwaysSample), benchRequest(b, ""))
}

// BenchmarkServeHTTP_TracedRootUnsampled is the same request against a tracer
// that records nothing. It is the row a caller running a low sampling ratio
// pays on the overwhelming majority of requests.
func BenchmarkServeHTTP_TracedRootUnsampled(b *testing.B) {
	serveBench(b, benchTracedHandler(b, svctrace.NeverSample), benchRequest(b, ""))
}

// BenchmarkServeHTTP_TracedChildSampled carries a valid inbound traceparent with
// the sampled bit set — the shape of every request in a traced deployment that
// is not the first hop. Extract parses the header; the trace id is inherited and
// the sampler is never consulted.
func BenchmarkServeHTTP_TracedChildSampled(b *testing.B) {
	handler := benchTracedHandler(b, svctrace.ParentBased(svctrace.NeverSample))
	serveBench(b, handler, benchRequest(b, sampledTraceParent))
}

// BenchmarkServeHTTP_TracedChildUnsampled carries the same header with the
// sampled bit CLEAR. The decision propagated from the root, so this span is a
// no-op — and the header is still parsed, which is what this row prices against
// the sampled child.
func BenchmarkServeHTTP_TracedChildUnsampled(b *testing.B) {
	handler := benchTracedHandler(b, svctrace.ParentBased(svctrace.AlwaysSample))
	serveBench(b, handler, benchRequest(b, unsampledTraceParent))
}
