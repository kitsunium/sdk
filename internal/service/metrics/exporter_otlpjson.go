// Package metrics — OTLP/JSON encoder: SnapshotValue to the bytes an OTLP
// receiver accepts, implemented from the specification with encoding/json.
package metrics

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"strconv"
	"sync"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// otlpJSONExporterName is the registered name of the default OTLP/JSON
// exporter, which writes to stderr (ADR 0030). It names the ENCODING, not the
// protocol, because an OTLP/protobuf encoder would register beside it.
const otlpJSONExporterName coremetrics.ExporterName = "otlpjson"

// The three AggregationTemporality enum values, copied from
// opentelemetry/proto/metrics/v1/metrics.proto. OTLP/JSON encodes an enum as
// its INTEGER, never as its name (specification §JSON Protobuf Encoding), which
// is the one place OTLP/JSON deviates from the generic proto3 JSON mapping.
const (
	// otlpTemporalityUnspecified is AGGREGATION_TEMPORALITY_UNSPECIFIED. The
	// schema's own comment reads "UNSPECIFIED is the default
	// AggregationTemporality, it MUST not be used" — so this constant names
	// the value this encoder REFUSES, never one it emits.
	otlpTemporalityUnspecified int = iota
	// otlpTemporalityDelta is AGGREGATION_TEMPORALITY_DELTA.
	otlpTemporalityDelta
	// otlpTemporalityCumulative is AGGREGATION_TEMPORALITY_CUMULATIVE.
	otlpTemporalityCumulative
)

// The three spellings proto3 JSON gives a non-finite double. A double is
// "a number or one of the special string values 'NaN', 'Infinity', and
// '-Infinity'", so a NaN gauge reading is expressible rather than fatal —
// unlike encoding/json's own float path, which refuses it outright.
const (
	otlpNaN         string = `"NaN"`
	otlpPosInfinity string = `"Infinity"`
	otlpNegInfinity string = `"-Infinity"`
)

// otlpDocumentTerminator ends each document a WRITER-bound OTLP exporter emits,
// so a stream of exports is newline-delimited JSON. It is deliberately NOT part
// of what EncodeOTLPJSON returns: that is the HTTP body, and a body is one
// document.
const otlpDocumentTerminator byte = '\n'

// otlpIntBufferSize pre-sizes a quoted 64-bit decimal: 20 digits, a sign, two
// quotes, rounded up.
const otlpIntBufferSize int = 24

// floatFmt / floatPrec are the strconv parameters for a shortest-round-trip
// double, the same pair the Prometheus connector formats its values with.
const (
	floatFmt  byte = 'g'
	floatPrec int  = -1
)

// OTLPJSON is the default OTLP/JSON exporter, registered to write each snapshot
// to stderr as one newline-terminated document. Use NewOTLPJSONExporter for a
// custom writer/name, EncodeOTLPJSON for the bytes alone, or
// NewOTLPHTTPExporter to actually ship them to a collector.
//
// stderr, not stdout, for the reason ADR 0030 gives in full on Text: importing
// a package must never arm a writer on a stream the process may be using as a
// protocol channel. This instance is a diagnostic — the production path is
// NewOTLPHTTPExporter, which is never registered because arming a network
// client on import would be strictly worse than arming a writer.
var OTLPJSON = coremetrics.RegisterExporter(newOTLPJSONExporter(otlpJSONExporterName, os.Stderr))

// otlpJSONExporter writes each snapshot to dst as one OTLP/JSON document.
//
// It is the encoder bound to an io.Writer and nothing more — no network, no
// retry, no endpoint. That separation is the point: a caller who wants the
// bytes calls EncodeOTLPJSON, a caller who wants them on a stream binds this,
// and a caller who wants them at a collector binds NewOTLPHTTPExporter. Each
// surface fails in exactly one way.
//
// mu serialises the single dst.Write for the reason textExporter's does —
// core/metrics.Exporter requires concurrency safety and dst is caller-supplied.
// Encoding happens outside the lock, so a slow writer serialises callers
// without also serialising the work.
type otlpJSONExporter struct {
	mu   sync.Mutex
	name coremetrics.ExporterName
	dst  io.Writer
}

// otlpInt64 is a signed 64-bit integer rendered as a DECIMAL STRING.
//
// That is the proto3 JSON mapping OTLP inherits — "64-bit integer numbers in
// JSON-encoded payloads are encoded as decimal strings" — and the reason is
// range, not taste: a JSON number is a double in most parsers, so an int64 past
// 2^53 loses its low bits on the way through. Every 64-bit field of this
// payload uses it, including the two timestamps, which are always past 2^53.
type otlpInt64 int64

// otlpUint64 is an unsigned 64-bit integer rendered as a decimal string, for
// the same reason otlpInt64 is: fixed64 and uint64 both map to a string.
type otlpUint64 uint64

// otlpDouble is an IEEE-754 double rendered as a JSON number, or as one of the
// three quoted spellings proto3 JSON gives a non-finite value.
//
// encoding/json refuses NaN and ±Inf outright ("json: unsupported value"), so
// without this type a single NaN gauge reading would fail the whole export.
// The model allows the value — a gauge is whatever was sampled — and the
// specification says how to spell it, so the encoder spells it.
type otlpDouble float64

// EncodeOTLPJSON renders snap as ONE OTLP/JSON ExportMetricsServiceRequest —
// exactly the bytes that go in the body of a POST to /v1/metrics under
// Content-Type: application/json.
//
// It is the encoder half of this package's OTLP support and it does no I/O at
// all, so it is usable and testable on its own: hand it a snapshot, compare the
// bytes to the schema. The emitter half (NewOTLPHTTPExporter) calls exactly
// this function and adds only the transport.
//
// The snapshot maps onto the payload without a regrouping pass, which is what
// ADR 0044 §Decision 9 shaped it for:
//
//	SnapshotValue        -> resourceMetrics[0]
//	  .Resource          ->   .resource.attributes
//	  .Scope             ->   .scopeMetrics[0].scope
//	  .Sums[name]        ->   .scopeMetrics[0].metrics[] {name, sum{…}}
//	  .Gauges[name]      ->   metrics[] {name, gauge{…}}
//	  .Histograms[name]  ->   metrics[] {name, histogram{…}}
//	  .StartTime/.Time   ->   copied DOWN onto every data point
//
// The two single-element levels are single because one Meter has exactly one
// Resource and one Scope.
//
// It REFUSES rather than emits a payload the schema cannot express: a
// temporality that is neither delta nor cumulative (OTLPUnresolvedTemporality)
// and a bucket layout that is not strictly-increasing finite bounds with one
// count per bucket plus the overflow (OTLPInvalidBucketLayout). Both are
// STRUCTURE — a temporality is fixed at meter construction, a bucket ladder is
// a literal at the call site — so each fails on the first export or never,
// exactly like the metric name the Prometheus connector refuses.
func EncodeOTLPJSON(snap coremetrics.SnapshotValue) (doc []byte, err error) {
	//: build the flat metrics list first; every refusal happens here, before
	//: a single byte is produced.
	list, buildErr := otlpMetricList(snap)
	//: an unrepresentable temporality or bucket ladder aborts the whole
	//: document — a half-payload would be a snapshot missing series, and a
	//: receiver cannot tell that from series that stopped existing.
	if buildErr != nil {
		//: surface the typed refusal.
		return nil, buildErr
	}
	//: the two collapsed levels are literal single-element slices: one Meter
	//: is one Resource and one Scope, so there is nothing to group. Each level
	//: is a named local, so no composite literal nests deeper than it reads.
	scope := otlpScopeMetrics{
		Scope:   otlpScope{Name: snap.Scope.Name, Version: snap.Scope.Version},
		Metrics: list,
	}
	resource := otlpResourceMetrics{
		Resource:     otlpResource{Attributes: otlpAttrs(snap.Resource.Attrs)},
		ScopeMetrics: []otlpScopeMetrics{scope},
	}
	//: marshal the tree with HTML escaping off — see marshalOTLPJSON.
	return marshalOTLPJSON(otlpRequest{ResourceMetrics: []otlpResourceMetrics{resource}})
}

// marshalOTLPJSON renders request with HTML escaping DISABLED and no trailing
// newline.
//
// encoding/json escapes '<', '>' and '&' into their \u00xx forms by default, a
// defence for JSON embedded in a <script> element. An OTLP body is never
// embedded in HTML, and OTel-conventional attributes carry URLs (url.full,
// http.route) whose query separators are exactly '&' — so the default turns a
// readable payload into an unreadable one for no gain, and makes this SDK's
// bytes differ from every other OTLP producer's for the same input.
// json.Encoder is the only way to turn it off, and it appends a newline that a
// single-document body must not carry.
func marshalOTLPJSON(request otlpRequest) (doc []byte, err error) {
	//: encode into a local buffer so the escaping switch is available.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	//: '&' in a URL attribute stays '&'.
	encoder.SetEscapeHTML(false)
	//: the only failure mode left is a type encoding/json cannot render, and
	//: every field of this tree is a Go primitive or a Marshaler defined here.
	if encodeErr := encoder.Encode(request); encodeErr != nil {
		//: report it typed rather than swallowing an impossible case.
		return nil, errs.Wrap(encodeErr, errs.WrapParams{
			Code:    coremetrics.CodeExportFailed,
			Reason:  "EXPORT_FAILED",
			Public:  "The metrics exporter failed to ship the snapshot",
			Private: "service/metrics: the OTLP/JSON encoder could not render the payload",
		})
	}
	//: Encode appends a newline; the HTTP body is one document, so drop it.
	return bytes.TrimSuffix(buf.Bytes(), []byte{otlpDocumentTerminator}), nil
}

// otlpMetricList flattens the snapshot's three name-keyed maps into the single
// metrics[] array a ScopeMetrics carries.
//
// Sums, then gauges, then histograms, each in ascending name order — the same
// walk the text exporter takes, and the reason a snapshot renders
// byte-identically twice in a row. OTLP itself imposes no order on metrics[];
// determinism is what makes the output diffable and its tests writable.
func otlpMetricList(snap coremetrics.SnapshotValue) (list []otlpMetric, err error) {
	//: the window is one pair per snapshot and is copied down onto each point.
	start, end := otlpUnixNano(snap.StartTime), otlpUnixNano(snap.Time)
	//: sums first — Counters and UpDownCounters share this map.
	list, err = appendOTLPSums(list, snap.Sums, start, end)
	//: abort the whole document on a refusal.
	if err != nil {
		//: surface the typed refusal.
		return nil, err
	}
	//: then gauges, which carry no temporality to refuse.
	list = appendOTLPGauges(list, snap.Gauges, start, end)
	//: then histograms, the only family with a bucket ladder to validate.
	list, err = appendOTLPHistograms(list, snap.Histograms, start, end)
	//: same abort.
	if err != nil {
		//: surface the typed refusal.
		return nil, err
	}
	//: hand back the flat list.
	return list, nil
}

// appendOTLPSums renders each sum name as one Metric carrying a Sum message.
//
// A Counter and an UpDownCounter both land here, told apart by isMonotonic —
// the OTel model's own economy, and the reason there is one point shape for two
// instruments.
func appendOTLPSums(list []otlpMetric, sums map[string]coremetrics.SumMetricValue, start, end otlpUint64) (metrics []otlpMetric, err error) {
	//: names first, so the document is stable across runs.
	for _, name := range sortedKeys(sums) {
		metric := sums[name]
		//: a temporality the enum cannot spell aborts before any output.
		temporality, tempErr := otlpTemporality(name, metric.Temporality)
		//: surface the typed refusal.
		if tempErr != nil {
			//: nothing is emitted.
			return nil, tempErr
		}
		//: one Metric per name, with the Sum envelope carrying the two facts
		//: the OTel model attaches to the metric rather than to a point.
		list = append(list, otlpMetric{Name: name, Sum: &otlpSum{
			DataPoints:             otlpSumPoints(metric.Points, start, end),
			AggregationTemporality: temporality,
			IsMonotonic:            metric.Monotonic,
		}})
	}
	//: hand back the extended list.
	return list, nil
}

// otlpSumPoints renders one sum name's series as NumberDataPoints carrying
// asInt, because a sum in this SDK is an int64 accumulator.
func otlpSumPoints(series []coremetrics.SumValue, start, end otlpUint64) []otlpNumberDataPoint {
	//: exactly-sized: one data point per series.
	points := make([]otlpNumberDataPoint, 0, len(series))
	//: the series are already sorted by attribute set by Collect.
	for _, point := range series {
		//: asInt is a oneof member, so it is emitted even when the total is 0.
		points = append(points, otlpNumberDataPoint{
			StartTimeUnixNano: start,
			TimeUnixNano:      end,
			AsInt:             new(otlpInt64(point.Value)),
			Attributes:        otlpAttrs(point.Attrs),
		})
	}
	//: hand back the rendered points.
	return points
}

// appendOTLPGauges renders each gauge name as one Metric carrying a Gauge
// message. A Gauge has no aggregationTemporality field — a sampled reading
// covers no window — so there is nothing here to refuse.
func appendOTLPGauges(list []otlpMetric, gauges map[string]coremetrics.GaugeMetricValue, start, end otlpUint64) []otlpMetric {
	//: same name-ordered walk as sums.
	for _, name := range sortedKeys(gauges) {
		//: one Metric per name; the Gauge envelope has a single field.
		list = append(list, otlpMetric{Name: name, Gauge: &otlpGauge{
			DataPoints: otlpGaugePoints(gauges[name].Points, start, end),
		}})
	}
	//: hand back the extended list.
	return list
}

// otlpGaugePoints renders one gauge name's series as NumberDataPoints carrying
// asDouble, because a gauge reading is a float64.
func otlpGaugePoints(series []coremetrics.GaugeValue, start, end otlpUint64) []otlpNumberDataPoint {
	//: exactly-sized: one data point per series.
	points := make([]otlpNumberDataPoint, 0, len(series))
	//: one point per series, in the snapshot's canonical order.
	for _, point := range series {
		//: asDouble is a oneof member, so it is emitted even at 0.
		points = append(points, otlpNumberDataPoint{
			StartTimeUnixNano: start,
			TimeUnixNano:      end,
			AsDouble:          new(otlpDouble(point.Value)),
			Attributes:        otlpAttrs(point.Attrs),
		})
	}
	//: hand back the rendered points.
	return points
}

// appendOTLPHistograms renders each histogram name as one Metric carrying a
// Histogram message of explicit-bucket data points.
func appendOTLPHistograms(list []otlpMetric, histograms map[string]coremetrics.HistogramMetricValue, start, end otlpUint64) (metrics []otlpMetric, err error) {
	//: same name-ordered walk as sums.
	for _, name := range sortedKeys(histograms) {
		metric := histograms[name]
		//: a temporality the enum cannot spell aborts before any output.
		temporality, tempErr := otlpTemporality(name, metric.Temporality)
		//: surface the typed refusal.
		if tempErr != nil {
			//: nothing is emitted.
			return nil, tempErr
		}
		//: a ladder OTLP cannot express aborts the whole document too.
		points, pointsErr := otlpHistogramPoints(name, metric.Points, start, end)
		//: surface the typed refusal.
		if pointsErr != nil {
			//: nothing is emitted.
			return nil, pointsErr
		}
		//: one Metric per name; a Histogram carries a temporality, no
		//: monotonicity — a distribution is not a total.
		list = append(list, otlpMetric{Name: name, Histogram: &otlpHistogram{
			DataPoints:             points,
			AggregationTemporality: temporality,
		}})
	}
	//: hand back the extended list.
	return list, nil
}

// otlpHistogramPoints renders one histogram name's series, validating each
// bucket ladder first.
//
// Counts ride through unchanged: the snapshot stores them PER BUCKET and OTLP's
// bucketCounts is per bucket too, so unlike the Prometheus connector — which
// has to build a cumulative ladder as it walks — there is nothing to convert.
func otlpHistogramPoints(name string, series []coremetrics.HistogramValue, start, end otlpUint64) (points []otlpHistogramDataPoint, err error) {
	//: exactly-sized: one data point per series.
	rendered := make([]otlpHistogramDataPoint, 0, len(series))
	//: one point per series, each with its own bucket ladder to check.
	for _, point := range series {
		//: a ladder OTLP cannot express aborts before any output.
		if layoutErr := checkOTLPBucketLayout(name, point); layoutErr != nil {
			//: surface the typed refusal.
			return nil, layoutErr
		}
		//: sum is an OPTIONAL double in the schema, so its presence is
		//: meaningful and this encoder always has one to state.
		rendered = append(rendered, otlpHistogramDataPoint{
			StartTimeUnixNano: start,
			TimeUnixNano:      end,
			Count:             otlpUint64(point.Count),
			Sum:               new(otlpDouble(point.Sum)),
			BucketCounts:      otlpBucketCounts(point.Counts),
			ExplicitBounds:    otlpExplicitBounds(point.Bounds),
			Attributes:        otlpAttrs(point.Attrs),
		})
	}
	//: hand back the rendered points.
	return rendered, nil
}

// otlpTemporality maps a Temporality onto its AggregationTemporality integer,
// and refuses everything else.
//
// UNSPECIFIED is not mapped to 0, although 0 is what it means: the schema says
// that value "MUST not be used", and a receiver handed it either drops the
// metric or guesses. Guessing is exactly the failure ADR 0044 exists to
// prevent — the same number under the two settings is two different facts — so
// this encoder refuses instead. A Meter resolves the knob at construction, so a
// snapshot can only carry an unresolved temporality when it was hand-built or
// cast; both are programmer errors, and both fail on the first export.
func otlpTemporality(name string, temporality coremetrics.Temporality) (value int, err error) {
	//: one branch per value the enum can spell.
	switch temporality {
	//: (T1,T2] — the window since the previous collection.
	case coremetrics.TemporalityDelta:
		//: AGGREGATION_TEMPORALITY_DELTA.
		return otlpTemporalityDelta, nil
	//: (T0,Tn] — the window since the series started.
	case coremetrics.TemporalityCumulative:
		//: AGGREGATION_TEMPORALITY_CUMULATIVE.
		return otlpTemporalityCumulative, nil
	//: TemporalityUnspecified, or a value cast into existence.
	default:
		//: name the metric, which is structure and safe to echo.
		return otlpTemporalityUnspecified, errs.Wrap(OTLPUnresolvedTemporality, errs.WrapParams{}, errs.String("metric", name))
	}
}

// checkOTLPBucketLayout refuses a histogram point whose buckets the schema
// cannot express.
//
// The schema states the invariant in the comments this checks, one for one:
// "The number of elements in bucket_counts array must be by one greater than
// the number of elements in explicit_bounds array", and "The values in the
// explicit_bounds array must be strictly increasing". A non-finite bound
// violates the second by construction — the bucket above the last declared
// bound is ALREADY (bound, +infinity), so declaring +Inf as a bound creates a
// second, permanently empty bucket covering the same range, and NaN is not an
// ordering at all.
//
// This is where OTLP and the Prometheus connector diverge on the same input.
// Prometheus SKIPS a non-finite bound, which it can afford because le="+Inf" is
// a mandatory separate line there; skipping here would break the count relation
// above, so the loss is named and refused instead of silently encoded.
func checkOTLPBucketLayout(name string, point coremetrics.HistogramValue) error {
	//: one count per declared bucket, plus the implicit +Inf overflow slot.
	if len(point.Counts) != len(point.Bounds)+1 {
		//: name the metric, which is structure and safe to echo.
		return errs.Wrap(OTLPInvalidBucketLayout, errs.WrapParams{}, errs.String("metric", name))
	}
	//: -Inf opens the ladder, so the first finite bound always exceeds it.
	previous := math.Inf(-1)
	//: strictly increasing AND finite, in one pass. NaN fails every ordered
	//: comparison, so it would slip past "<=" unnoticed without its own test.
	for _, bound := range point.Bounds {
		//: refuse a bound that is not a real number, or that does not advance.
		if math.IsNaN(bound) || math.IsInf(bound, 0) || bound <= previous {
			//: name the metric, which is structure and safe to echo.
			return errs.Wrap(OTLPInvalidBucketLayout, errs.WrapParams{}, errs.String("metric", name))
		}
		//: advance the ladder.
		previous = bound
	}
	//: the layout is expressible.
	return nil
}

// otlpAttrs renders an attribute set as the repeated KeyValue every OTLP
// message spells its dimensions with. An empty set stays nil, which
// encoding/json omits — the proto3 rule for an empty repeated field.
func otlpAttrs(attrs []coremetrics.AttrValue) []otlpKeyValue {
	//: the dimensionless series has no attributes array at all.
	if len(attrs) == 0 {
		//: omitted by the omitempty tag.
		return nil
	}
	//: exactly-sized; the set is already sorted by Key.
	pairs := make([]otlpKeyValue, len(attrs))
	//: one KeyValue per dimension, in the snapshot's canonical order.
	for i, attr := range attrs {
		//: the AnyValue oneof carries the TYPE OTLP has and Prometheus lacks.
		pairs[i] = otlpKeyValueOf(attr)
	}
	//: hand back the rendered set.
	return pairs
}

// otlpKeyValueOf maps one typed attribute onto a KeyValue whose AnyValue names
// the attribute's kind.
//
// Exactly one AnyValue field is non-nil, and it is emitted even when it holds
// the type's zero: a oneof member has explicit presence, so an absent field
// means "no case selected", not "the default". Bool("cache.hit", false) must
// therefore encode as {"boolValue":false} and not as {}.
func otlpKeyValueOf(attr coremetrics.AttrValue) otlpKeyValue {
	//: one oneof case per attribute kind.
	switch attr.Kind() {
	//: the common dimension.
	case coremetrics.AttrKindString:
		//: stringValue.
		return otlpKeyValue{Key: attr.Key, Value: otlpAnyValue{StringValue: new(attr.Str())}}
	//: a flag.
	case coremetrics.AttrKindBool:
		//: boolValue — false is a value, not an absence.
		return otlpKeyValue{Key: attr.Key, Value: otlpAnyValue{BoolValue: new(attr.Bool())}}
	//: a signed 64-bit integer, which rides as a decimal string.
	case coremetrics.AttrKindInt64:
		//: intValue.
		return otlpKeyValue{Key: attr.Key, Value: otlpAnyValue{IntValue: new(otlpInt64(attr.Int64()))}}
	//: an IEEE-754 double, non-finite values included.
	case coremetrics.AttrKindFloat64:
		//: doubleValue.
		return otlpKeyValue{Key: attr.Key, Value: otlpAnyValue{DoubleValue: new(otlpDouble(attr.Float64()))}}
	//: AttrKindInvalid never reaches a snapshot — the meter panics on it at
	//: the call site that wrote it.
	default:
		//: an empty AnyValue selects no case, which is what "no value" is.
		return otlpKeyValue{Key: attr.Key}
	}
}

// otlpBucketCounts widens the snapshot's per-bucket counts into the 64-bit
// decimal strings the wire wants. The values are unchanged: the snapshot is
// already per bucket, which is what OTLP asks for.
func otlpBucketCounts(counts []uint64) []otlpUint64 {
	//: a histogram always has at least the overflow slot, but a hand-built
	//: point could be empty and an empty repeated field is omitted.
	if len(counts) == 0 {
		//: omitted by the omitempty tag.
		return nil
	}
	//: exactly-sized copy in wire order.
	ladder := make([]otlpUint64, len(counts))
	//: one bucket count per slot, verbatim.
	for i, count := range counts {
		//: only the JSON rendering differs.
		ladder[i] = otlpUint64(count)
	}
	//: hand back the rendered ladder.
	return ladder
}

// otlpExplicitBounds renders the declared upper bounds. The field is
// explicit_bounds in the schema, which is why the snapshot calls it Bounds
// rather than Buckets.
func otlpExplicitBounds(bounds []float64) []otlpDouble {
	//: a histogram with no declared bound is one +Inf bucket, and an empty
	//: repeated field is omitted.
	if len(bounds) == 0 {
		//: omitted by the omitempty tag.
		return nil
	}
	//: exactly-sized copy in ascending order.
	ladder := make([]otlpDouble, len(bounds))
	//: every bound is finite here — checkOTLPBucketLayout ran first.
	for i, bound := range bounds {
		//: only the JSON rendering differs.
		ladder[i] = otlpDouble(bound)
	}
	//: hand back the rendered ladder.
	return ladder
}

// otlpUnixNano converts a wall-clock instant to the fixed64 nanosecond
// timestamp OTLP carries, mapping an unset instant onto 0.
//
// The guard is not defensive noise. time.Time's own documentation says
// UnixNano's "result is undefined if the Unix time in nanoseconds cannot be
// represented by an int64", and the zero Time is exactly that case: it returns
// a large negative number which, cast to uint64, becomes a timestamp several
// centuries in the future. A snapshot built by hand rather than by a Meter is
// the reachable path, and it would ship silently wrong rather than visibly
// empty. Zero is what the schema already means by an unknown timestamp.
func otlpUnixNano(instant time.Time) otlpUint64 {
	//: the unset instant, and a pre-1970 one, have no unsigned spelling.
	if instant.IsZero() || instant.UnixNano() < 0 {
		//: the schema's own "unknown" value, rather than a wrapped one.
		return 0
	}
	//: in range and positive.
	return otlpUint64(instant.UnixNano())
}

// newOTLPJSONExporter is the shared constructor.
func newOTLPJSONExporter(name coremetrics.ExporterName, dst io.Writer) *otlpJSONExporter {
	//: a stateless writer-bound exporter.
	return &otlpJSONExporter{name: name, dst: dst}
}

// NewOTLPJSONExporter returns an Exporter writing each snapshot to dst as one
// newline-terminated OTLP/JSON document. It is NOT added to the registry — bind
// it yourself or call Export directly.
//
// The newline makes a stream of exports newline-delimited JSON, which is what a
// file or a terminal wants; EncodeOTLPJSON returns the document alone, which is
// what an HTTP body wants.
func NewOTLPJSONExporter(name coremetrics.ExporterName, dst io.Writer) coremetrics.Exporter {
	//: hand back the concrete exporter behind the interface.
	return newOTLPJSONExporter(name, dst)
}

// Name implements core/metrics.Exporter.
func (e *otlpJSONExporter) Name() coremetrics.ExporterName {
	//: the registered name.
	return e.name
}

// Export encodes the whole snapshot, then writes it once, so a refusal leaves
// dst untouched and the only remaining error surface is the single write.
func (e *otlpJSONExporter) Export(snap coremetrics.SnapshotValue) error {
	//: encode + validate first; nothing is written if either fails.
	doc, err := EncodeOTLPJSON(snap)
	//: an unrepresentable temporality or bucket ladder is reported typed.
	if err != nil {
		//: the sentinel already carries the code, reason and public message.
		return err
	}
	//: terminate the document so consecutive exports do not run together.
	doc = append(doc, otlpDocumentTerminator)
	//: single write — serialised so concurrent Exports cannot interleave
	//: partial documents into a non-atomic dst.
	e.mu.Lock()
	_, writeErr := e.dst.Write(doc)
	e.mu.Unlock()
	//: success fast-path.
	if writeErr == nil {
		//: snapshot written.
		return nil
	}
	//: wrap the writer fault with the dotted-quad code.
	return errs.Wrap(writeErr, errs.WrapParams{
		Code:    coremetrics.CodeExportFailed,
		Reason:  "EXPORT_FAILED",
		Public:  "The metrics exporter failed to ship the snapshot",
		Private: "service/metrics: OTLP/JSON exporter writer returned an error",
	})
}

// MarshalJSON renders the integer as a quoted decimal string.
func (v otlpInt64) MarshalJSON() (encoded []byte, err error) {
	//: quote, digits, quote — no escaping is possible inside a decimal.
	out := make([]byte, 0, otlpIntBufferSize)
	out = append(out, '"')
	out = strconv.AppendInt(out, int64(v), decimalBase)
	//: json.Marshal never sees an error from a decimal rendering.
	return append(out, '"'), nil
}

// MarshalJSON renders the integer as a quoted decimal string.
func (v otlpUint64) MarshalJSON() (encoded []byte, err error) {
	//: quote, digits, quote.
	out := make([]byte, 0, otlpIntBufferSize)
	out = append(out, '"')
	out = strconv.AppendUint(out, uint64(v), decimalBase)
	//: json.Marshal never sees an error from a decimal rendering.
	return append(out, '"'), nil
}

// MarshalJSON renders the double shortest-round-trip, or names it when it is
// not finite.
func (v otlpDouble) MarshalJSON() (encoded []byte, err error) {
	//: a value that is not a number has a name rather than a rendering.
	value := float64(v)
	//: NaN first — it fails every ordered comparison below.
	if math.IsNaN(value) {
		//: the schema's spelling, quoted.
		return []byte(otlpNaN), nil
	}
	//: +Inf.
	if math.IsInf(value, 1) {
		//: the schema's spelling, quoted.
		return []byte(otlpPosInfinity), nil
	}
	//: -Inf.
	if math.IsInf(value, -1) {
		//: the schema's spelling, quoted.
		return []byte(otlpNegInfinity), nil
	}
	//: finite: shortest representation that round-trips, which is a JSON
	//: number in every form strconv produces (123, 1.5, 1e+21, -1.5e-08).
	return strconv.AppendFloat(nil, value, floatFmt, floatPrec, floatBitSize), nil
}
