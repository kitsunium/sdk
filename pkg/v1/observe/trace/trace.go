//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/observe/trace .

// Package trace is distributed tracing: the third pillar of observability beside
// [github.com/kitsunium/sdk/pkg/v1/observe/logger] and
// [github.com/kitsunium/sdk/pkg/v1/observe/metrics].
//
// It speaks the OpenTelemetry trace data model and the W3C Trace Context
// propagation format, and imports NOTHING from go.opentelemetry.io. OTel is a
// published specification; this SDK implements it from the document, exactly as
// it implements the Prometheus exposition format, RFC 7517 and a five-field
// POSIX cron. What OTel buys is interoperability, and interoperability is a
// property of the wire, not of the import graph — a span exported by this
// package is accepted by any OTLP collector, and the dependency budget is zero.
//
// # The whole thing
//
//	rec := trace.NewRecorder(trace.RecorderConfig{
//		Resource: trace.Resource{Attrs: []trace.Attr{
//			trace.String(trace.ServiceNameKey, "checkout"),
//		}},
//	})
//	tracer := trace.NewTracer(trace.TracerConfig{
//		Resource: rec.Resource(),
//		Sink:     rec.Sink(),
//	})
//
//	ctx, span := tracer.Start(ctx, "charge", trace.SpanParams{Kind: trace.KindInternal})
//	defer span.End()
//	span.SetAttrs(trace.Int64("amount.cents", 1299))
//
// [Recorder].Collect drains the finished spans; [EncodeOTLPJSON] turns them into
// the bytes an OTLP collector accepts, and [NewOTLPHTTPExporter] POSTs them.
//
// # Sampling is decided ONCE, at the root
//
// A [Sampler] is consulted only when a trace STARTS. Every child span — in this
// process and in every process downstream — inherits the answer through the
// `sampled` bit of the traceparent header.
//
// That is not an optimisation. Deciding per span produces a trace with holes in
// it, and a trace with holes is worse than no trace at all: a span whose parent
// was dropped becomes an orphan the backend renders as its own root, so one
// request appears as several unrelated ones and the latency of the whole is
// unrecoverable. The failure is invisible at the process that causes it.
//
// [AlwaysSample], [NeverSample], [ParentBased] and [Ratio] are the four
// policies. [Ratio] REFUSES a fraction of exactly 0 — see its documentation for
// why that is the one input a sampling rate must not accept.
//
// # Propagation is W3C Trace Context, and a bad header never fails a request
//
// [Extract] reads a span context out of anything with http.Header's Get/Set
// pair, and returns the invalid zero value when the header is absent, malformed,
// or carries an identifier the specification declares invalid. It returns no
// error, because the specification prescribes exactly one response — start a new
// trace — and the header is written by a stranger. Rejecting a request over it
// would be a denial of service with extra steps.
//
// [ParseTraceParent] is the typed-error form, for a caller who is diagnosing
// rather than serving.
//
// # Middlewares
//
// [ServerMiddleware] and [ClientMiddleware] are the SDK's own middleware type
// instantiated at http.Handler and http.RoundTripper, so they compose with the
// network domain's Chain:
//
//	srv.Group("api", server.Listen("tcp", ":8443")).
//		HandleHTTP(net.Chain(mux, trace.ServerMiddleware(tracer)))
//
//	c.HTTP().Transport = net.Chain(c.HTTP().Transport, trace.ClientMiddleware(tracer))
//
// The client one lives at the transport for the same reason the network domain's
// policy does: there is no path to the network that skips it.
//
// # What it deliberately does not do
//
// It does not batch. A [SpanSink] receives one span at a time and a batching
// processor is a sink that buffers and forwards — which is where its cost is
// visible, rather than hidden inside a tracer.
//
// It does not schedule an export.
// [github.com/kitsunium/sdk/pkg/v1/app/scheduler] already owns "when", and Collect
// plus Export is one call.
//
// It does not register the OTLP/HTTP exporter. Arming a network client from an
// import is a step beyond the stdout hazard the SDK already refuses, and there is
// no endpoint that could be a correct default.
//
// See ADR 0051.
package trace
