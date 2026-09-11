//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/metrics .

// Package metrics is the public facade for the SDK's observability domain — the
// natural twin of the logger, shaped on the OpenTelemetry metrics DATA MODEL.
//
// The model is OpenTelemetry's; the code is not. Nothing here imports
// go.opentelemetry.io: OTel is a published specification, and this SDK
// implements it the way it implements the Prometheus exposition format or
// RFC 7517 — from the document, with the standard library. What you get is the
// model's vocabulary (typed attributes, aggregation temporality, Resource,
// InstrumentationScope, monotonic and non-monotonic sums, observable
// instruments) with this SDK's dependency budget, which is zero.
//
//	m := metrics.NewMeter()
//	m.Counter("requests").Add(1)
//	_ = metrics.Export("text", m.Collect())
//
// # Instruments
//
// A [Meter] mints four synchronous instruments and registers three
// asynchronous ones:
//
//   - [Counter] — a MONOTONIC sum. Requests served, bytes written. A
//     non-positive Add is ignored, because a backend reading a monotonic sum
//     is entitled to treat a decrease as a process restart.
//   - [UpDownCounter] — a NON-MONOTONIC sum. In-flight requests, queue depth.
//     Additive like a counter, but Add may be negative and Dec exists.
//   - [Gauge] — a sampled reading with no arithmetic behind it. Temperature,
//     a configured limit.
//   - [Histogram] — a bucketed distribution. Latency, payload size.
//   - [FullMeter.ObservableCounter], [FullMeter.ObservableUpDownCounter] and
//     [FullMeter.ObservableGauge] — a callback read once per [Meter.Collect],
//     for a value that already exists somewhere and only needs reading
//     (runtime.NumGoroutine(), a cache size). The callback reports the
//     ABSOLUTE value; the SDK differences it when the meter is a delta reader.
//
// A Counter and an UpDownCounter both produce a [SumMetric] in the snapshot,
// told apart by [SumMetric.Monotonic]. That is the OTel data model's own
// economy: monotonicity is a FIELD of a sum, not a second point type.
//
// [Meter] itself carries only the three instruments and Collect it shipped
// with; UpDownCounter and the observables live on the sibling interfaces
// [UpDownMeter] and [AsyncMeter], because a published Go interface cannot grow
// a method without breaking every downstream implementer (ADR 0039).
// [FullMeter] is the union, and it is what [NewMeter] returns.
//
// # Describing an instrument
//
// The OTel data model gives a metric a human-readable description, and the SDK
// records it against the instrument NAME rather than against a call site,
// because that is where the model puts it and because it is explicitly
// NON-IDENTIFYING: describing a metric never creates a series. [Describer] is a
// fourth sibling and, unlike the other two, it is NOT folded into [FullMeter] —
// a union is still an interface, and widening one breaks downstream doubles at
// any version. Reach it by type assertion:
//
//	meter := metrics.NewMeter()
//	if d, ok := meter.(metrics.Describer); ok {
//		d.Describe("http_server_requests", "Requests served, by route and status")
//	}
//
// The false branch is information, not boilerplate: a Meter that records no
// description does not implement [Describer], and that is how a caller learns
// their documentation will not reach the wire.
//
// Describing is a WIRING-TIME call and costs the observation path nothing. Two
// mistakes panic, both of them structural and therefore caught on the first
// boot or never: an EMPTY description ([InvalidDescription]), which would
// document nothing; and a SECOND, DIFFERENT description for one name
// ([DescriptionConflict]), because a description belongs to the name and two of
// them means one wiring site is wrong. Re-describing with identical text is
// idempotent.
//
// What each exporter does with it: "prometheus" emits `# HELP <name> <text>`
// above `# TYPE` and nothing at all when there is no description, "otlpjson"
// fills `Metric.description` and omits the field when it is empty, and "text"
// prints it on the `# metric` header because that exporter exists to show the
// whole model.
//
// # Attributes
//
// An instrument is identified by its name AND its attributes. One name plus one
// attribute set is one SERIES, and every fetch of that pair returns the same
// instrument, so observations accumulate in one place wherever they are made.
// An attribute's value is TYPED — string, bool, int64 or float64 — and built by
// one of four constructors:
//
//	m.Counter("requests",
//	    metrics.String("http.request.method", "GET"),
//	    metrics.Int64("http.response.status_code", 503),
//	    metrics.Bool("cache.hit", false),
//	).Inc()
//
// The type is part of the identity: String("v", "1") and Int64("v", 1) are two
// different series, not one. Attribute ORDER is not — a set is a set. Passing
// no attributes names the dimensionless series.
//
// An attribute KEY is structure: it is written at the call site and constant
// for the process, so an empty key, a repeated key, or a value built by a
// struct literal instead of a constructor is a programmer error and panics with
// [InvalidAttribute]. An attribute VALUE is data and may be anything.
//
// Homogeneous ARRAY attributes, which the OTel model also allows, are not
// implemented — see the package's CLAUDE.md for what they would cost on the
// observation path.
//
// # Temporality
//
// [Temporality] says which window a reported number covers, and it is the one
// fact a metric value cannot carry by itself:
//
//   - [TemporalityCumulative] — the point covers everything since the meter
//     started. Successive collections repeat the start timestamp. This is what
//     an unconfigured [Meter] does, because an in-memory meter accumulates into
//     atomics and never resets them.
//   - [TemporalityDelta] — the point covers only the window since the previous
//     collection. [Meter.Collect] then CONSUMES what it reports, so a delta
//     meter has exactly one reader.
//
// [TemporalityUnspecified] is the zero value and resolves to cumulative; there
// is no setting that leaves it undecided (ADR 0031).
//
// # Resource and scope
//
// A [Snapshot] carries two identities once for the whole payload rather than on
// every point: the [Resource] (who produced this — service.name, and anything
// else the caller adds) and the [Scope] (what instrumented it — a library name
// and version). An absent service.name resolves to [UnknownService], which is
// what the specification mandates rather than a value this SDK invented.
//
//	m := metrics.NewMeterWithConfig(metrics.MeterConfig{
//	    Resource: metrics.Resource{Attrs: []metrics.Attr{
//	        metrics.String(metrics.ServiceNameKey, "orders"),
//	    }},
//	    Scope: metrics.Scope{Name: "github.com/acme/orders", Version: "1.4.0"},
//	})
//
// # Cardinality
//
// Distinct attribute values create distinct series, and an unbounded stream of
// them is a memory incident rather than a reporting inconvenience. Every Meter
// therefore bounds how many series ONE instrument name may hold —
// [DefaultMaxSeriesPerInstrument] unless [NewMeterWithConfig] says otherwise.
// Past the bound, further attribute sets are folded into a single aggregated
// series carrying [OverflowAttrKey]=true: memory stays bounded, no observation
// is dropped, and the condition is visible in every snapshot from then on. What
// is lost is the breakdown — once folded, an observation's own attributes are
// gone.
//
// A non-positive MaxSeriesPerInstrument clamps to the default. There is no
// setting that means "unbounded" (ADR 0031); a caller who wants a very large
// bound writes a very large number, where a reviewer can see it.
//
// # Exporting
//
// A [Snapshot] maps each instrument name to its metric, and each metric to its
// series — the shape every per-series wire format wants, and the shape an
// OTLP encoder can walk without a regrouping pass. Three stdlib exporters are
// registered on import and [Export] dispatches by name. All three write to
// stderr so that importing this package never arms a writer on stdout, which a
// process may be using as a protocol channel (ADR 0030).
//
// "text" is a diagnostic that prints the whole model — resource, scope, window,
// temporality, monotonicity, and each attribute with its type visible.
//
// "prometheus" is a deliberately LOSSY connector to the Prometheus text
// exposition format, kept because a Prometheus deployment is a real
// destination and not because the format can carry this model. What it loses:
//
//   - Temporality. The format has none, and a server reads every counter as
//     cumulative. A delta snapshot is REFUSED ([UnsupportedTemporality]) rather
//     than mis-labelled, and so is a hand-built one whose temporality was left
//     unresolved, which would otherwise pass as cumulative.
//   - The attribute's TYPE. A Prometheus label value is a string, so
//     Int64("v", 1) and String("v", "1") — two series here — become one there.
//   - The Resource and the Scope, which have nowhere to go. In particular
//     service.name cannot even be spelled: a Prometheus label name is
//     [a-zA-Z_][a-zA-Z0-9_]* and the dot is outside it.
//   - Exemplars, which this SDK does not produce at all.
//
// It REFUSES, rather than rewrites, a name the format cannot spell: an
// instrument name must match [a-zA-Z_:][a-zA-Z0-9_:]* and an attribute key
// [a-zA-Z_][a-zA-Z0-9_]*, which means an OTel-conventional dotted key is
// refused too. Mapping the offending characters to "_" would silently merge two
// distinct instruments into one family, and a metrics pipeline cannot detect
// that afterwards. The refusal is typed ([InvalidMetricName],
// [InvalidLabelName], [ReservedLabelName]) and leaves the writer untouched,
// because a truncated exposition parses as a complete one.
//
// A scrape endpoint binds its own exporter with [NewPrometheusExporter] and
// hands it the response writer:
//
//	http.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
//	    w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
//	    _ = metrics.NewPrometheusExporter("scrape", w).Export(m.Collect())
//	})
//
// A non-monotonic sum is typed `gauge` there, not `counter`, because `rate()`
// on a counter re-extrapolates from zero every time the value falls.
//
// # OTLP
//
// "otlpjson" is the native wire: the OpenTelemetry Protocol, JSON encoding,
// implemented from the specification with encoding/json and net/http and
// nothing else. Nothing here imports go.opentelemetry.io, for the reason
// nothing else in this package does — OTel is a document, and the whole point
// of shaping [Snapshot] on its data model was that a payload carrying
// temporality, resource, scope and typed attributes is OTLP-encodable by
// construction. Unlike the Prometheus connector, this one loses NOTHING.
//
// It comes in two halves, deliberately, so an encoding bug and a network bug
// are never the same investigation:
//
// [EncodeOTLPJSON] is the ENCODER. It takes a [Snapshot] and returns the exact
// bytes of one ExportMetricsServiceRequest — the body of a POST to
// /v1/metrics — and does no I/O at all. Hand it to a queue, a file, a
// compressor, or a test that compares it to the schema:
//
//	body, err := metrics.EncodeOTLPJSON(m.Collect())
//
// [NewOTLPJSONExporter] binds that encoder to an io.Writer, emitting one
// newline-terminated document per export so a stream is NDJSON. The registered
// "otlpjson" exporter is that, on stderr (ADR 0030).
//
// [NewOTLPHTTPExporter] is the EMITTER: it encodes and POSTs to a collector
// under Content-Type: application/json. It is NOT registered — the registry is
// reached by importing a package, there is no endpoint that could be a correct
// default, and arming a network client from an import is worse than arming a
// writer. The endpoint is a full URL used as-is, so spell the signal path:
//
//	exporter, err := metrics.NewOTLPHTTPExporter("otlp",
//	    metrics.OTLPHTTPConfig{Endpoint: "http://collector:4318" + metrics.OTLPMetricsPath})
//
// It does not retry, and that is a decision. The specification asks a client to
// back off on a retryable status; this SDK already ships that policy, and a
// backoff hidden inside Export would be a second one you cannot see, tune or
// cancel. What you get instead is the classification a retry policy needs —
// [OTLPRetryable] has exactly the signature resilience.RetryConfig.Retryable
// wants:
//
//	runner := resilience.NewRetry(resilience.RetryConfig{
//	    MaxAttempts: 3,
//	    BaseDelay:   time.Second,
//	    Retryable:   metrics.OTLPRetryable,
//	})
//
// [OTLPExportUnavailable] is transient (a transport fault, or HTTP 429 / 502 /
// 503 / 504 — the four the specification lists, and no other 5xx).
// [OTLPExportRejected] is permanent. [OTLPPartialSuccess] means the collector
// accepted the request and dropped some of its points, which the specification
// forbids retrying, so it is reported rather than replayed.
//
// Two things the encoder REFUSES rather than mis-encodes, both structural and
// therefore wrong on the first export or never:
// [OTLPUnresolvedTemporality], because the schema says its UNSPECIFIED value
// "MUST not be used"; and [OTLPInvalidBucketLayout], because OTLP requires
// bucket counts one longer than strictly-increasing finite bounds — the bucket
// above the last declared bound is already the +Inf overflow, so an infinite
// bound would declare it twice.
package metrics

import (
	"io"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

const (
	// DefaultMaxSeriesPerInstrument is the cardinality bound NewMeter applies,
	// and the value a non-positive MeterConfig knob clamps to.
	DefaultMaxSeriesPerInstrument int = svcmetrics.DefaultMaxSeriesPerInstrument
	// OverflowAttrKey is the reserved attribute naming a meter's aggregated
	// overflow series. It appears in a snapshot only once an instrument has
	// exceeded its cardinality bound, and its value is the boolean true.
	OverflowAttrKey string = coremetrics.OverflowAttrKey
	// ServiceNameKey is the attribute key the OTel resource semantic
	// conventions reserve for the logical name of the service.
	ServiceNameKey string = coremetrics.ServiceNameKey
	// UnknownService is the service.name the specification mandates when a
	// producer supplies none.
	UnknownService string = coremetrics.UnknownService
	// DefaultScopeName names this SDK as the instrumenting library when a
	// caller declares no scope of their own.
	DefaultScopeName string = coremetrics.DefaultScopeName
	// OTLPMetricsPath is the URL path OTLP/HTTP reserves for the metrics
	// signal. An OTLP endpoint is a full URL used as-is, so append this to a
	// collector's base address rather than hoping the SDK guesses it.
	OTLPMetricsPath string = svcmetrics.OTLPMetricsPath
	// DefaultOTLPTimeout bounds one OTLP/HTTP export round trip when
	// OTLPHTTPConfig leaves Timeout unset. It is the OpenTelemetry protocol
	// exporter specification's own default, not a figure this SDK invented.
	DefaultOTLPTimeout time.Duration = svcmetrics.DefaultOTLPTimeout
	// DefaultOTLPMaxResponseBytes caps how much of a collector's response is
	// read. The body is the one length a remote party controls in this
	// exchange, and a conforming response is a few hundred bytes.
	DefaultOTLPMaxResponseBytes int64 = svcmetrics.DefaultOTLPMaxResponseBytes
)

// OTLPHTTPConfig is the public alias for an OTLP/HTTP exporter's configuration.
type OTLPHTTPConfig = svcmetrics.OTLPHTTPConfig

// Meter is the public alias for the frozen instrument factory: Counter, Gauge,
// Histogram and Collect.
type Meter = coremetrics.Meter

// UpDownMeter is the public alias for the sibling port that mints the
// non-monotonic sum Meter cannot.
type UpDownMeter = coremetrics.UpDownMeter

// AsyncMeter is the public alias for the sibling port that registers
// observable (asynchronous) instruments.
type AsyncMeter = coremetrics.AsyncMeter

// Describer is the public alias for the sibling port that documents an
// instrument NAME. It is deliberately NOT part of FullMeter — reach it by type
// assertion, and read the false case as "this meter records no description".
type Describer = coremetrics.Describer

// FullMeter is the public alias for the union of all three — what NewMeter
// returns.
type FullMeter = coremetrics.FullMeter

// Counter is the public alias for a monotonic sum instrument.
type Counter = coremetrics.Counter

// UpDownCounter is the public alias for a non-monotonic sum instrument.
type UpDownCounter = coremetrics.UpDownCounter

// Gauge is the public alias for a sampled-reading instrument.
type Gauge = coremetrics.Gauge

// Histogram is the public alias for a bucketed distribution instrument.
type Histogram = coremetrics.Histogram

// Attr is the public alias for one typed dimension of a series.
type Attr = coremetrics.AttrValue

// AttrKind is the public alias for an attribute value's type tag.
type AttrKind = coremetrics.AttrKind

// AttrKindInvalid marks an attribute whose value no constructor ever set.
const AttrKindInvalid AttrKind = coremetrics.AttrKindInvalid

// AttrKindString marks a string-valued attribute.
const AttrKindString AttrKind = coremetrics.AttrKindString

// AttrKindBool marks a bool-valued attribute.
const AttrKindBool AttrKind = coremetrics.AttrKindBool

// AttrKindInt64 marks a signed 64-bit integer attribute.
const AttrKindInt64 AttrKind = coremetrics.AttrKindInt64

// AttrKindFloat64 marks an IEEE-754 double attribute.
const AttrKindFloat64 AttrKind = coremetrics.AttrKindFloat64

// Temporality is the public alias for a metric's aggregation temporality.
type Temporality = coremetrics.Temporality

// TemporalityUnspecified is the unset zero value; it resolves to
// TemporalityCumulative and never reaches a Snapshot.
const TemporalityUnspecified Temporality = coremetrics.TemporalityUnspecified

// TemporalityDelta reports the window since the previous collection.
const TemporalityDelta Temporality = coremetrics.TemporalityDelta

// TemporalityCumulative reports the window since the series started.
const TemporalityCumulative Temporality = coremetrics.TemporalityCumulative

// Resource is the public alias for the producer's identity, carried once per
// snapshot.
type Resource = coremetrics.ResourceValue

// Scope is the public alias for the instrumentation's identity, carried once
// per snapshot.
type Scope = coremetrics.ScopeValue

// Snapshot is the public alias for a whole-meter point-in-time copy.
type Snapshot = coremetrics.SnapshotValue

// SumMetric is the public alias for every series of one sum instrument name,
// plus its temporality and monotonicity.
type SumMetric = coremetrics.SumMetricValue

// SumPoint is the public alias for one sum series.
type SumPoint = coremetrics.SumValue

// GaugeMetric is the public alias for every series of one gauge instrument name.
type GaugeMetric = coremetrics.GaugeMetricValue

// GaugePoint is the public alias for one gauge series.
type GaugePoint = coremetrics.GaugeValue

// HistogramMetric is the public alias for every series of one histogram
// instrument name, plus its temporality.
type HistogramMetric = coremetrics.HistogramMetricValue

// HistogramPoint is the public alias for one histogram series.
type HistogramPoint = coremetrics.HistogramValue

// ObserveInt64 is the public alias for the reporting function an integer
// observable callback is handed.
type ObserveInt64 = coremetrics.ObserveInt64

// ObserveFloat64 is the public alias for the reporting function a double
// observable callback is handed.
type ObserveFloat64 = coremetrics.ObserveFloat64

// Int64Callback is the public alias for an integer observable's callback.
type Int64Callback = coremetrics.Int64Callback

// Float64Callback is the public alias for a double observable's callback.
type Float64Callback = coremetrics.Float64Callback

// MeterConfig is the public alias for a Meter's configuration.
type MeterConfig = svcmetrics.MeterConfig

// Exporter is the public alias for a snapshot shipper.
type Exporter = coremetrics.Exporter

// ExporterName is the public alias for an exporter's registry key.
type ExporterName = coremetrics.ExporterName

var (
	// UnknownExporter is returned by Export when no exporter matches the name.
	UnknownExporter = coremetrics.UnknownExporter
	// ExportFailed wraps an exporter's shipping failure.
	ExportFailed = coremetrics.ExportFailed
	// InstrumentKindConflict is raised when a name is reused across kinds.
	InstrumentKindConflict = coremetrics.InstrumentKindConflict
	// InvalidAttribute is raised when an attribute set has an empty or
	// repeated key, or a value no constructor ever set.
	InvalidAttribute = coremetrics.InvalidAttribute
	// InvalidTemporality is raised when a MeterConfig carries a Temporality
	// that is none of the three constants.
	InvalidTemporality = coremetrics.InvalidTemporality
	// InvalidDescription is raised when Describe is handed an empty
	// description, which would document nothing.
	InvalidDescription = coremetrics.InvalidDescription
	// DescriptionConflict is raised when a second, DIFFERENT description is
	// bound to an instrument name that already has one.
	DescriptionConflict = coremetrics.DescriptionConflict
	// InvalidMetricName is returned by the Prometheus connector when an
	// instrument name is not a valid Prometheus metric name.
	InvalidMetricName = svcmetrics.InvalidMetricName
	// InvalidLabelName is returned by the Prometheus connector when an
	// attribute key is not a valid Prometheus label name.
	InvalidLabelName = svcmetrics.InvalidLabelName
	// ReservedLabelName is returned by the Prometheus connector when an
	// attribute key is legal but reserved — a "__" prefix, or "le" on a
	// histogram.
	ReservedLabelName = svcmetrics.ReservedLabelName
	// UnsupportedTemporality is returned by the Prometheus connector when the
	// snapshot is not cumulative — a delta one, which the exposition format
	// cannot express, or a hand-built one whose temporality is unresolved.
	UnsupportedTemporality = svcmetrics.UnsupportedTemporality
	// OTLPUnresolvedTemporality is returned by the OTLP/JSON encoder when a
	// metric's temporality is neither delta nor cumulative. The schema's
	// UNSPECIFIED value is documented as one that MUST NOT be used.
	OTLPUnresolvedTemporality = svcmetrics.OTLPUnresolvedTemporality
	// OTLPInvalidBucketLayout is returned by the OTLP/JSON encoder when a
	// histogram's buckets are not one count more than strictly-increasing
	// finite bounds.
	OTLPInvalidBucketLayout = svcmetrics.OTLPInvalidBucketLayout
	// OTLPEndpointInvalid is returned by NewOTLPHTTPExporter when the endpoint
	// is not an absolute http(s) URL carrying the signal path.
	OTLPEndpointInvalid = svcmetrics.OTLPEndpointInvalid
	// OTLPExportRejected is returned when a collector refuses the payload with
	// a status the specification marks non-retryable.
	OTLPExportRejected = svcmetrics.OTLPExportRejected
	// OTLPExportUnavailable is returned when an OTLP/HTTP export fails
	// transiently — a transport fault, or HTTP 429/502/503/504. It is the one
	// OTLPRetryable reports true for.
	OTLPExportUnavailable = svcmetrics.OTLPExportUnavailable
	// OTLPPartialSuccess is returned when a collector accepts the request but
	// rejects some of its data points. The specification forbids retrying it.
	OTLPPartialSuccess = svcmetrics.OTLPPartialSuccess
)

// String returns a string-valued attribute. It is the shortest of the four
// constructors to write because a string dimension is the common one.
func String(key, value string) Attr {
	//: delegate to the core constructor.
	return coremetrics.String(key, value)
}

// Bool returns a bool-valued attribute.
func Bool(key string, value bool) Attr {
	//: delegate to the core constructor.
	return coremetrics.Bool(key, value)
}

// Int64 returns a signed-integer attribute.
func Int64(key string, value int64) Attr {
	//: delegate to the core constructor.
	return coremetrics.Int64(key, value)
}

// Float64 returns a double attribute. A float is a measurement rather than a
// dimension, and it is keyed on its bit pattern — see the core documentation.
func Float64(key string, value float64) Attr {
	//: delegate to the core constructor.
	return coremetrics.Float64(key, value)
}

// NewMeter returns a fresh in-memory Meter with every MeterConfig knob at its
// resolved default: bounded at DefaultMaxSeriesPerInstrument, cumulative,
// service.name=unknown_service, this SDK as the scope, and the system clock.
func NewMeter() FullMeter {
	//: delegate to the service in-memory meter.
	return svcmetrics.NewMeter()
}

// NewMeterWithConfig returns a fresh in-memory Meter honouring cfg. Every unset
// field resolves to a working value and none of them to an inert one.
func NewMeterWithConfig(cfg MeterConfig) FullMeter {
	//: delegate to the service in-memory meter.
	return svcmetrics.NewMeterWithConfig(cfg)
}

// NewTextExporter returns a text Exporter writing to dst under name (not
// auto-registered).
func NewTextExporter(name ExporterName, dst io.Writer) Exporter {
	//: delegate to the service constructor.
	return svcmetrics.NewTextExporter(name, dst)
}

// NewPrometheusExporter returns an Exporter rendering the Prometheus text
// exposition format to dst under name (not auto-registered). Hand it the
// http.ResponseWriter of a /metrics handler; the registered "prometheus"
// exporter targets stderr and is a diagnostic, not a scrape endpoint.
func NewPrometheusExporter(name ExporterName, dst io.Writer) Exporter {
	//: delegate to the service constructor.
	return svcmetrics.NewPrometheusExporter(name, dst)
}

// EncodeOTLPJSON renders snap as ONE OTLP/JSON ExportMetricsServiceRequest:
// exactly the bytes that go in the body of a POST to /v1/metrics under
// Content-Type: application/json.
//
// It does no I/O, so it is the half of OTLP support that is testable on its
// own — and the half NewOTLPHTTPExporter calls before it touches a socket.
func EncodeOTLPJSON(snap Snapshot) (doc []byte, err error) {
	//: delegate to the service encoder.
	return svcmetrics.EncodeOTLPJSON(snap)
}

// NewOTLPJSONExporter returns an Exporter writing each snapshot to dst as one
// newline-terminated OTLP/JSON document, so a stream of exports is NDJSON. It
// is not auto-registered; the registered "otlpjson" exporter is this, on
// stderr.
func NewOTLPJSONExporter(name ExporterName, dst io.Writer) Exporter {
	//: delegate to the service constructor.
	return svcmetrics.NewOTLPJSONExporter(name, dst)
}

// NewOTLPHTTPExporter returns an Exporter that encodes each snapshot as
// OTLP/JSON and POSTs it to cfg.Endpoint, which is a full URL used as-is.
//
// It is never registered, and it never retries — compose resilience.NewRetry
// with OTLPRetryable instead. A refused endpoint fails here, at wiring, rather
// than at the first scrape.
func NewOTLPHTTPExporter(name ExporterName, cfg OTLPHTTPConfig) (exporter Exporter, err error) {
	//: delegate to the service constructor.
	return svcmetrics.NewOTLPHTTPExporter(name, cfg)
}

// OTLPRetryable reports whether err is an OTLP/HTTP failure the specification
// says may be replayed: a transport fault, or HTTP 429 / 502 / 503 / 504.
// Its signature is resilience.RetryConfig.Retryable's, so it drops in without
// an adapter.
func OTLPRetryable(err error) bool {
	//: delegate to the service classifier.
	return svcmetrics.OTLPRetryable(err)
}

// RegisterExporter adds e to the process-wide exporter registry.
func RegisterExporter(e Exporter) Exporter {
	//: delegate to the core registry.
	return coremetrics.RegisterExporter(e)
}

// Export ships snap through the exporter registered as name.
func Export(name ExporterName, snap Snapshot) error {
	//: delegate to the core dispatch.
	return coremetrics.Export(name, snap)
}

// AvailableExporters returns the sorted list of registered exporter names.
func AvailableExporters() []ExporterName {
	//: delegate to the core registry.
	return coremetrics.AvailableExporters()
}
