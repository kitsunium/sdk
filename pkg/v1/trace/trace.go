//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/trace .

// Package trace is distributed tracing: the third pillar of observability beside
// [github.com/kitsunium/sdk/pkg/v1/logger] and
// [github.com/kitsunium/sdk/pkg/v1/metrics].
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
// [Recorder.Collect] drains the finished spans; [EncodeOTLPJSON] turns them into
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
// [github.com/kitsunium/sdk/pkg/v1/scheduler] already owns "when", and Collect
// plus Export is one call.
//
// It does not register the OTLP/HTTP exporter. Arming a network client from an
// import is a step beyond the stdout hazard the SDK already refuses, and there is
// no endpoint that could be a correct default.
//
// See ADR 0051.
package trace

import (
	"context"
	"io"
	"net/http"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/trace"
)

// The five span kinds. The zero value resolves to KindInternal. Each is declared
// on its own so the group carries no implied sequence: these are the schema's
// numbers, not this package's.

// KindUnspecified is SPAN_KIND_UNSPECIFIED; it resolves to KindInternal.
const KindUnspecified Kind = coretrace.SpanKindUnspecified

// KindInternal is work inside one process.
const KindInternal Kind = coretrace.SpanKindInternal

// KindServer is handling an inbound synchronous request.
const KindServer Kind = coretrace.SpanKindServer

// KindClient is making an outbound synchronous request.
const KindClient Kind = coretrace.SpanKindClient

// KindProducer is enqueuing a message handled later, elsewhere.
const KindProducer Kind = coretrace.SpanKindProducer

// KindConsumer is handling a message enqueued earlier.
const KindConsumer Kind = coretrace.SpanKindConsumer

// StatusUnset is the default: no explicit judgement. It does NOT mean "it
// worked" — a backend applies its own heuristics, and claiming OK on the
// caller's behalf would suppress them.
const StatusUnset StatusCode = coretrace.StatusUnset

// StatusOK is an explicit success assertion by the developer.
const StatusOK StatusCode = coretrace.StatusOK

// StatusError is a failure.
const StatusError StatusCode = coretrace.StatusError

// AttrKindInvalid is the zero value and names no type.
const AttrKindInvalid AttrKind = coremetrics.AttrKindInvalid

// AttrKindString marks a string-valued attribute.
const AttrKindString AttrKind = coremetrics.AttrKindString

// AttrKindBool marks a bool-valued attribute.
const AttrKindBool AttrKind = coremetrics.AttrKindBool

// AttrKindInt64 marks a signed 64-bit integer attribute.
const AttrKindInt64 AttrKind = coremetrics.AttrKindInt64

// AttrKindFloat64 marks an IEEE-754 double attribute.
const AttrKindFloat64 AttrKind = coremetrics.AttrKindFloat64

// TraceParentHeader is the W3C header carrying the span context.
const TraceParentHeader string = coretrace.TraceParentHeader

// TraceStateHeader is the W3C header carrying the vendor list.
const TraceStateHeader string = coretrace.TraceStateHeader

// TraceParentLen is the length of a version-00 traceparent: 55 characters.
const TraceParentLen int = coretrace.TraceParentLen

// VersionSupported is the traceparent version this SDK emits: "00".
const VersionSupported string = coretrace.VersionSupported

// FlagSampled is the traceparent sampled bit — the one flag version 00 defines,
// and the one the whole sampling design travels in.
const FlagSampled TraceFlags = coretrace.FlagSampled

// MaxTraceStateMembers is the W3C list cap: 32 members, from the grammar itself
// rather than from a ceiling this SDK invented.
const MaxTraceStateMembers int = coretrace.MaxTraceStateMembers

// DefaultScopeName names this package as the instrumenting library when a caller
// declares no scope. It is deliberately NOT the metrics one: a scope names the
// library that produced THIS signal.
const DefaultScopeName string = coretrace.DefaultScopeName

// ServiceNameKey is the resource attribute a backend joins a trace to a metric
// on. It is the same constant pkg/v1/metrics exports, so the two signals cannot
// disagree about the spelling.
const ServiceNameKey string = coremetrics.ServiceNameKey

// ExceptionEventName is the reserved event name RecordError writes.
const ExceptionEventName string = coretrace.ExceptionEventName

// ExceptionTypeKey names a recorded error's stable identity.
const ExceptionTypeKey string = coretrace.ExceptionTypeKey

// ExceptionMessageKey names a recorded error's message.
const ExceptionMessageKey string = coretrace.ExceptionMessageKey

// HTTPRequestMethodKey is the request method attribute the middlewares record.
const HTTPRequestMethodKey string = svctrace.HTTPRequestMethodKey

// HTTPResponseStatusCodeKey is the response status attribute.
const HTTPResponseStatusCodeKey string = svctrace.HTTPResponseStatusCodeKey

// URLPathKey is the inbound request path attribute, in its ESCAPED form.
const URLPathKey string = svctrace.URLPathKey

// URLSchemeKey is the request scheme attribute.
const URLSchemeKey string = svctrace.URLSchemeKey

// URLFullKey is the outbound URL attribute, recorded on CLIENT spans.
const URLFullKey string = svctrace.URLFullKey

// ServerAddressKey is the host attribute.
const ServerAddressKey string = svctrace.ServerAddressKey

// OTLPTracesPath is the OTLP/HTTP path for the trace signal. Append it to a
// collector's base URL yourself, so the whole address is one readable string.
const OTLPTracesPath string = svctrace.OTLPTracesPath

// DefaultOTLPTimeout bounds one export round trip: 10s, the OpenTelemetry
// specification's own default for OTEL_EXPORTER_OTLP_TIMEOUT.
const DefaultOTLPTimeout time.Duration = svctrace.DefaultOTLPTimeout

// DefaultOTLPMaxResponseBytes caps the response body read: 1 MiB.
const DefaultOTLPMaxResponseBytes int64 = svctrace.DefaultOTLPMaxResponseBytes

// DefaultMaxSpans is a Recorder's default bound. There is no "unbounded"
// setting, because a buffer nobody drains is how a tracing integration takes a
// process down.
const DefaultMaxSpans int = svctrace.DefaultMaxSpans

// Sentinels returned by this package. Match with errors.Is or errs.HasCode.
var (
	// InvalidTraceParent reports a traceparent header that cannot be read.
	InvalidTraceParent = coretrace.InvalidTraceParent
	// InvalidTraceState reports a tracestate header that cannot be read.
	InvalidTraceState = coretrace.InvalidTraceState
	// InvalidSpanName is the panic sentinel for a Start with no name.
	InvalidSpanName = coretrace.InvalidSpanName
	// UnknownExporter reports a name no imported package registered.
	UnknownExporter = coretrace.UnknownExporter
	// ExportFailed wraps an exporter's failure.
	ExportFailed = coretrace.ExportFailed
	// DuplicateRegistration is the boot-time registry panic sentinel.
	DuplicateRegistration = coretrace.DuplicateRegistration
	// EntropyFailed reports a crypto/rand failure while minting an identifier.
	EntropyFailed = svctrace.EntropyFailed
	// InvalidSampleRatio reports a fraction Ratio will not accept — including
	// exactly 0, which also spells "unconfigured".
	InvalidSampleRatio = svctrace.InvalidSampleRatio
	// OTLPInvalidSpanContext reports a span with an all-zero identifier.
	OTLPInvalidSpanContext = svctrace.OTLPInvalidSpanContext
	// OTLPSpanNotEnded reports a span that reached the encoder unended.
	OTLPSpanNotEnded = svctrace.OTLPSpanNotEnded
	// OTLPEndpointInvalid reports an endpoint that cannot address a collector.
	OTLPEndpointInvalid = svctrace.OTLPEndpointInvalid
	// OTLPExportRejected reports a permanent refusal by the collector.
	OTLPExportRejected = svctrace.OTLPExportRejected
	// OTLPExportUnavailable reports a transient failure worth retrying.
	OTLPExportUnavailable = svctrace.OTLPExportUnavailable
	// OTLPPartialSuccess reports an accepted request with rejected spans.
	OTLPPartialSuccess = svctrace.OTLPPartialSuccess

	// The four attribute constructors below are the same functions
	// [github.com/kitsunium/sdk/pkg/v1/metrics] exports, so a value built by
	// either package is accepted by both.

	// String returns a string-valued attribute.
	String = coremetrics.String
	// Bool returns a bool-valued attribute.
	Bool = coremetrics.Bool
	// Int64 returns a signed-integer attribute.
	Int64 = coremetrics.Int64
	// Float64 returns a double attribute.
	Float64 = coremetrics.Float64
)

// ── The ports ────────────────────────────────────────────────────────────────

// Tracer starts spans. FROZEN at one method (ADR 0039).
type Tracer = coretrace.Tracer

// Span is one live, recording span. FROZEN at five methods (ADR 0039).
type Span = coretrace.Span

// Sampler decides whether a ROOT trace is recorded. A func port, so it cannot
// grow a method at all.
type Sampler = coretrace.Sampler

// SpanSink receives every sampled span at End. A func port, for the same reason.
type SpanSink = coretrace.SpanSink

// Carrier is the two-method surface a header set presents for propagation. It is
// exactly http.Header's Get/Set pair, so http.Header satisfies it with no
// adapter.
type Carrier = coretrace.Carrier

// SpanExporter ships a batch of finished spans to a backend.
type SpanExporter = coretrace.SpanExporter

// ExporterName is the typed key a SpanExporter registers under.
type ExporterName = coretrace.ExporterName

// ── The data model ───────────────────────────────────────────────────────────

// TraceID identifies one trace: sixteen octets, rendered as 32 lowercase hex
// digits. The all-zero value is invalid.
type TraceID = coretrace.TraceID

// SpanID identifies one span: eight octets, rendered as 16 lowercase hex
// digits. The all-zero value is invalid, and is also how "no parent" is spelled.
type SpanID = coretrace.SpanID

// TraceFlags is the traceparent flag byte. Only bit 0, sampled, is defined.
type TraceFlags = coretrace.TraceFlags

// SpanContext is the immutable identity of a span — what travels in a
// traceparent and what a child inherits.
type SpanContext = coretrace.SpanContextValue

// TraceState is the parsed tracestate header: an ordered vendor list, leftmost
// first. Immutable.
type TraceState = coretrace.StateValue

// Kind says how a span relates to its neighbours.
type Kind = coretrace.SpanKind

// StatusCode is a span's verdict: unset, ok or error.
type StatusCode = coretrace.StatusCode

// Status is a span's recorded outcome.
type Status = coretrace.StatusValue

// Event is a timestamped point inside a span.
type Event = coretrace.EventValue

// Link points at a causally related span in another trace.
type Link = coretrace.LinkValue

// SpanData is one FINISHED span — what a SpanSink receives and an exporter
// ships. The live one is [Span].
type SpanData = coretrace.SpanValue

// Spans is a batch of finished spans plus the Resource and Scope that describe
// all of them.
type Spans = coretrace.SpansValue

// Attr is one typed dimension. It is the SAME type as
// [github.com/kitsunium/sdk/pkg/v1/metrics.Attr], deliberately: an attribute is
// OTel's common.proto, shared by every signal, so a value built for a metric is
// accepted by a span and the reverse.
type Attr = coremetrics.AttrValue

// AttrKind discriminates an Attr's value type.
type AttrKind = coremetrics.AttrKind

// Resource identifies the producer of the telemetry. The same type a MeterConfig
// takes, so a process cannot carry two Resources that disagree about
// service.name — which is the key a backend correlates a trace with a metric on.
type Resource = coremetrics.ResourceValue

// Scope identifies the instrumentation that started the spans.
type Scope = coremetrics.ScopeValue

// SpanParams are the facts a span is born with. The zero value is an INTERNAL
// span starting now.
type SpanParams = coretrace.SpanParams

// SamplingParams is everything a Sampler sees.
type SamplingParams = coretrace.SamplingParams

// ── The implementations ──────────────────────────────────────────────────────

// Tracer configuration and the concrete tracer.
type (
	// TracerConfig configures a Tracer. Every field has a resolved meaning
	// when left unset.
	TracerConfig = svctrace.TracerConfig
	// Recorder accumulates finished spans in memory and hands them out in
	// batches. It is the in-tree SpanSink.
	Recorder = svctrace.Recorder
	// RecorderConfig configures a Recorder.
	RecorderConfig = svctrace.RecorderConfig
	// OTLPHTTPConfig configures the OTLP/HTTP exporter.
	OTLPHTTPConfig = svctrace.OTLPHTTPConfig
)

// NewTracer builds a Tracer from cfg, applying every clamp once. It cannot fail:
// every field of TracerConfig has a resolved meaning, and the inputs that CAN be
// refused — a sampling ratio, an OTLP endpoint — are refused by their own
// constructors before they reach here.
func NewTracer(cfg TracerConfig) *svctrace.Tracer {
	//: the service layer owns the wiring; this facade only forwards.
	return svctrace.NewTracer(cfg)
}

// NewRecorder builds the in-memory span destination. Its Sink is what a
// TracerConfig takes, and its Collect DRAINS — a Recorder has exactly one reader.
func NewRecorder(cfg RecorderConfig) *Recorder {
	//: the service layer owns the wiring; this facade only forwards.
	return svctrace.NewRecorder(cfg)
}

// ── Sampling ─────────────────────────────────────────────────────────────────

// AlwaysSample keeps every root trace.
func AlwaysSample(params SamplingParams) bool {
	//: the policy is in the service layer; this is its published name.
	return svctrace.AlwaysSample(params)
}

// NeverSample drops every root trace. It exists so "off" has a NAME a reviewer
// can grep for, rather than a rate of zero that is indistinguishable from a field
// nobody set.
func NeverSample(params SamplingParams) bool {
	//: the policy is in the service layer; this is its published name.
	return svctrace.NeverSample(params)
}

// ParentBased returns a Sampler that honours a valid parent's decision and
// consults root only at the start of a trace. It is what almost every deployment
// wants — see the package documentation on why re-deciding produces a trace with
// holes in it. A nil root clamps to AlwaysSample.
func ParentBased(root Sampler) Sampler {
	//: the policy is in the service layer; this is its published name.
	return svctrace.ParentBased(root)
}

// Ratio returns a Sampler keeping a deterministic fraction of root traces,
// decided on the trace id so two services reach the same verdict for the same id.
//
// It REFUSES exactly 0, and that refusal is the point. A float64 left unset, a
// JSON document missing the key and a YAML `rate:` with nothing after it all
// produce 0.0 — so a rate of zero means both "sample nothing" and "nobody
// configured this", and nothing in the type can tell them apart. The failure is
// silent: no error, no log, and no telemetry, where the absence of telemetry IS
// the symptom. Say NeverSample for none and AlwaysSample (or Ratio(1)) for all.
func Ratio(fraction float64) (sampler Sampler, err error) {
	//: the policy is in the service layer; this is its published name.
	return svctrace.Ratio(fraction)
}

// ── Propagation ──────────────────────────────────────────────────────────────

// Inject writes context into carrier as a traceparent, plus a tracestate when the
// vendor list is non-empty. An invalid context writes NOTHING — not an empty
// header, not a zero-filled one.
func Inject(context SpanContext, carrier Carrier) {
	//: the format lives in core; this facade only forwards.
	coretrace.Inject(context, carrier)
}

// Extract reads a span context out of carrier, returning the invalid zero value
// when there is nothing usable. It returns no error on purpose — see the package
// documentation.
func Extract(carrier Carrier) SpanContext {
	//: the format lives in core; this facade only forwards.
	return coretrace.Extract(carrier)
}

// ParseTraceParent reads a traceparent header value, with a typed error. It is
// the diagnostic form of Extract: use it when you are debugging a header, not
// when you are serving a request.
func ParseTraceParent(header string) (context SpanContext, err error) {
	//: the format lives in core; this facade only forwards.
	return coretrace.ParseTraceParent(header)
}

// FormatTraceParent renders a span context as a version-00 traceparent,
// reporting ok=false when the context names no joinable span — an all-zero
// identifier is a header every conforming receiver must ignore, so there is
// nothing to write. Undefined flag bits are masked here, which is where this SDK
// acts as a producer.
func FormatTraceParent(context SpanContext) (header string, ok bool) {
	//: the format lives in core; this facade only forwards.
	return coretrace.FormatTraceParent(context)
}

// ParseTraceState reads a tracestate header value. It refuses the whole header
// rather than salvaging the members it understood: a half-parsed list forwarded
// to the next hop is a list this process invented.
func ParseTraceState(header string) (state TraceState, err error) {
	//: the format lives in core; this facade only forwards.
	return coretrace.ParseTraceState(header)
}

// ParseTraceID reads 32 lowercase hex digits, refusing an all-zero result.
func ParseTraceID(text string) (id TraceID, err error) {
	//: the format lives in core; this facade only forwards.
	return coretrace.ParseTraceID(text)
}

// ParseSpanID reads 16 lowercase hex digits, refusing an all-zero result.
func ParseSpanID(text string) (id SpanID, err error) {
	//: the format lives in core; this facade only forwards.
	return coretrace.ParseSpanID(text)
}

// NewTraceID draws a fresh random trace identifier.
func NewTraceID() (id TraceID, err error) {
	//: identifier generation lives in the service layer.
	return svctrace.NewTraceID()
}

// NewSpanID draws a fresh random span identifier.
func NewSpanID() (id SpanID, err error) {
	//: identifier generation lives in the service layer.
	return svctrace.NewSpanID()
}

// ContextWithSpanContext returns a copy of parent carrying spanContext. An
// invalid context is still stored: it is how "this scope deliberately has no
// trace" is expressed, and it shadows any outer one.
func ContextWithSpanContext(parent context.Context, spanContext SpanContext) context.Context {
	//: context plumbing lives in core; this facade only forwards.
	return coretrace.ContextWithSpanContext(parent, spanContext)
}

// SpanContextFromContext returns the span context carried by ctx, or the invalid
// zero value when there is none.
func SpanContextFromContext(ctx context.Context) SpanContext {
	//: context plumbing lives in core; this facade only forwards.
	return coretrace.SpanContextFromContext(ctx)
}

// ── Instrumentation helpers ──────────────────────────────────────────────────

// RecordError records err on span as the conventional `exception` event and marks
// the span ERROR.
//
// It is a helper rather than a Span method because what an error's type is, and
// whether recording one should also set the status, are judgements about the
// caller's error model — and a port method would freeze this SDK's answer into
// every downstream implementation (ADR 0039).
func RecordError(span Span, err error) {
	//: the helper lives beside the port it decorates.
	svctrace.RecordError(span, err)
}

// ServerMiddleware returns a middleware tracing every inbound request: it
// extracts the parent context from the headers, starts a SERVER span named by the
// request method, and records the status. A malformed traceparent starts a new
// trace and never fails the request.
func ServerMiddleware(tracer Tracer) corenet.Middleware[http.Handler] {
	//: the middleware lives in the service layer; this facade only forwards.
	return svctrace.ServerMiddleware(tracer)
}

// ClientMiddleware returns a middleware tracing every outbound request and
// INJECTING the traceparent into it. It decorates the RoundTripper, so no call
// site can skip it, and it clones the request rather than mutating one the caller
// still holds.
func ClientMiddleware(tracer Tracer) corenet.Middleware[http.RoundTripper] {
	//: the middleware lives in the service layer; this facade only forwards.
	return svctrace.ClientMiddleware(tracer)
}

// ── Export ───────────────────────────────────────────────────────────────────

// EncodeOTLPJSON renders spans as ONE OTLP/JSON ExportTraceServiceRequest —
// exactly the bytes that go in the body of a POST to /v1/traces. It does no I/O,
// so an encoding question never becomes a network question.
func EncodeOTLPJSON(spans Spans) (doc []byte, err error) {
	//: the encoder lives in the service layer; this facade only forwards.
	return svctrace.EncodeOTLPJSON(spans)
}

// NewOTLPJSONExporter returns a SpanExporter writing each batch to dst as one
// newline-terminated OTLP/JSON document. It is not registered — bind it yourself.
func NewOTLPJSONExporter(name ExporterName, dst io.Writer) SpanExporter {
	//: the exporter lives in the service layer; this facade only forwards.
	return svctrace.NewOTLPJSONExporter(name, dst)
}

// NewOTLPHTTPExporter builds a SpanExporter that POSTs each batch to an OTLP
// collector. It is deliberately NOT in the registry: arming a network client from
// an import is worse than arming a writer, and no endpoint could be a correct
// default. It does not retry either — use OTLPRetryable with
// [github.com/kitsunium/sdk/pkg/v1/resilience].
func NewOTLPHTTPExporter(name ExporterName, cfg OTLPHTTPConfig) (exporter SpanExporter, err error) {
	//: the exporter lives in the service layer; this facade only forwards.
	return svctrace.NewOTLPHTTPExporter(name, cfg)
}

// OTLPRetryable reports whether err is an OTLP/HTTP failure the specification
// says may be replayed. Its signature is exactly resilience.RetryConfig.Retryable's.
func OTLPRetryable(err error) bool {
	//: the classifier lives in the service layer; this facade only forwards.
	return svctrace.OTLPRetryable(err)
}

// RegisterExporter inserts e under e.Name(). It panics on a nil exporter or a
// distinct exporter claiming a taken name.
func RegisterExporter(e SpanExporter) SpanExporter {
	//: the registry lives in core; this facade only forwards.
	return coretrace.RegisterExporter(e)
}

// LookupExporter returns the SpanExporter registered under name.
func LookupExporter(name ExporterName) (e SpanExporter, ok bool) {
	//: the registry lives in core; this facade only forwards.
	return coretrace.LookupExporter(name)
}

// AvailableExporters returns the sorted list of registered names. The OTLP/HTTP
// emitter never appears in it.
func AvailableExporters() []ExporterName {
	//: the registry lives in core; this facade only forwards.
	return coretrace.AvailableExporters()
}

// Export ships spans through the SpanExporter registered as name.
func Export(name ExporterName, spans Spans) error {
	//: the registry lives in core; this facade only forwards.
	return coretrace.Export(name, spans)
}
