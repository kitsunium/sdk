// Package metrics — OTLP/JSON encoder: SnapshotValue to the bytes an OTLP
// receiver accepts, implemented from the specification with encoding/json.
package metrics

import (
	"io"
	"math"
	"os"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/internal/otlp"
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

var (
	// otlpEncodeFailure is the wrap a rendering fault in the shared marshal leaves
	// under: this signal's EXPORT_FAILED, in its own words. Near-impossible — every
	// field of the tree is a Go primitive or one of the shared Marshalers — and
	// still typed rather than swallowed.
	otlpEncodeFailure = errs.WrapParams{
		Code:    coremetrics.CodeExportFailed,
		Reason:  "EXPORT_FAILED",
		Public:  "The metrics exporter failed to ship the snapshot",
		Private: "service/metrics: the OTLP/JSON encoder could not render the payload",
	}

	// otlpWriteFailure is the wrap a writer fault leaves under when the
	// writer-bound exporter emits a document: this signal's EXPORT_FAILED.
	otlpWriteFailure = errs.WrapParams{
		Code:    coremetrics.CodeExportFailed,
		Reason:  "EXPORT_FAILED",
		Public:  "The metrics exporter failed to ship the snapshot",
		Private: "service/metrics: OTLP/JSON exporter writer returned an error",
	}

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
	OTLPJSON = coremetrics.RegisterExporter(newOTLPJSONExporter(otlpJSONExporterName, os.Stderr))
)

// otlpJSONExporter writes each snapshot to dst as one OTLP/JSON document.
//
// It is the encoder bound to an io.Writer and nothing more — no network, no
// retry, no endpoint. That separation is the point: a caller who wants the
// bytes calls EncodeOTLPJSON, a caller who wants them on a stream binds this,
// and a caller who wants them at a collector binds NewOTLPHTTPExporter. Each
// surface fails in exactly one way.
//
// The shared stream serialises the single write for the reason textExporter's
// mutex does — core/metrics.Exporter requires concurrency safety and the
// writer is caller-supplied. Encoding happens before it, outside the lock, so
// a slow writer serialises callers without also serialising the work.
type otlpJSONExporter struct {
	// name is the exporter's registry key.
	name coremetrics.ExporterName
	// stream is the newline-delimited document stream bound to the writer.
	stream *otlp.Stream
}

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
//	  .*[name].Description ->  metrics[].description, omitted when empty
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
		Scope:   otlp.ScopeOf(snap.Scope),
		Metrics: list,
	}
	resource := otlpResourceMetrics{
		Resource:     otlp.ResourceOf(snap.Resource),
		ScopeMetrics: []otlpScopeMetrics{scope},
	}
	//: marshal the tree with HTML escaping off — see otlp.Marshal.
	return otlp.Marshal(otlpRequest{ResourceMetrics: []otlpResourceMetrics{resource}}, &otlpEncodeFailure)
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
	start, end := otlp.UnixNano(snap.StartTime), otlp.UnixNano(snap.Time)
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
func appendOTLPSums(list []otlpMetric, sums map[string]coremetrics.SumMetricValue, start, end otlp.Uint64) (metrics []otlpMetric, err error) {
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
		list = append(list, otlpMetric{Name: name, Description: metric.Description, Sum: &otlpSum{
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
func otlpSumPoints(series []coremetrics.SumValue, start, end otlp.Uint64) []otlpNumberDataPoint {
	//: exactly-sized: one data point per series.
	points := make([]otlpNumberDataPoint, 0, len(series))
	//: the series are already sorted by attribute set by Collect.
	for _, point := range series {
		//: asInt is a oneof member, so it is emitted even when the total is 0.
		points = append(points, otlpNumberDataPoint{
			StartTimeUnixNano: start,
			TimeUnixNano:      end,
			AsInt:             new(otlp.Int64(point.Value)),
			Attributes:        otlp.Attrs(point.Attrs),
		})
	}
	//: hand back the rendered points.
	return points
}

// appendOTLPGauges renders each gauge name as one Metric carrying a Gauge
// message. A Gauge has no aggregationTemporality field — a sampled reading
// covers no window — so there is nothing here to refuse.
func appendOTLPGauges(list []otlpMetric, gauges map[string]coremetrics.GaugeMetricValue, start, end otlp.Uint64) []otlpMetric {
	//: same name-ordered walk as sums.
	for _, name := range sortedKeys(gauges) {
		//: one Metric per name; the Gauge envelope has a single field.
		list = append(list, otlpMetric{Name: name, Description: gauges[name].Description, Gauge: &otlpGauge{
			DataPoints: otlpGaugePoints(gauges[name].Points, start, end),
		}})
	}
	//: hand back the extended list.
	return list
}

// otlpGaugePoints renders one gauge name's series as NumberDataPoints carrying
// asDouble, because a gauge reading is a float64.
func otlpGaugePoints(series []coremetrics.GaugeValue, start, end otlp.Uint64) []otlpNumberDataPoint {
	//: exactly-sized: one data point per series.
	points := make([]otlpNumberDataPoint, 0, len(series))
	//: one point per series, in the snapshot's canonical order.
	for _, point := range series {
		//: asDouble is a oneof member, so it is emitted even at 0.
		points = append(points, otlpNumberDataPoint{
			StartTimeUnixNano: start,
			TimeUnixNano:      end,
			AsDouble:          new(otlp.Double(point.Value)),
			Attributes:        otlp.Attrs(point.Attrs),
		})
	}
	//: hand back the rendered points.
	return points
}

// appendOTLPHistograms renders each histogram name as one Metric carrying a
// Histogram message of explicit-bucket data points.
func appendOTLPHistograms(list []otlpMetric, histograms map[string]coremetrics.HistogramMetricValue, start, end otlp.Uint64) (metrics []otlpMetric, err error) {
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
		list = append(list, otlpMetric{Name: name, Description: metric.Description, Histogram: &otlpHistogram{
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
func otlpHistogramPoints(name string, series []coremetrics.HistogramValue, start, end otlp.Uint64) (points []otlpHistogramDataPoint, err error) {
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
			Count:             otlp.Uint64(point.Count),
			Sum:               new(otlp.Double(point.Sum)),
			BucketCounts:      otlpBucketCounts(point.Counts),
			ExplicitBounds:    otlpExplicitBounds(point.Bounds),
			Attributes:        otlp.Attrs(point.Attrs),
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

// otlpBucketCounts widens the snapshot's per-bucket counts into the 64-bit
// decimal strings the wire wants. The values are unchanged: the snapshot is
// already per bucket, which is what OTLP asks for.
func otlpBucketCounts(counts []uint64) []otlp.Uint64 {
	//: a histogram always has at least the overflow slot, but a hand-built
	//: point could be empty and an empty repeated field is omitted.
	if len(counts) == 0 {
		//: omitted by the omitempty tag.
		return nil
	}
	//: exactly-sized copy in wire order.
	ladder := make([]otlp.Uint64, len(counts))
	//: one bucket count per slot, verbatim.
	for i, count := range counts {
		//: only the JSON rendering differs.
		ladder[i] = otlp.Uint64(count)
	}
	//: hand back the rendered ladder.
	return ladder
}

// otlpExplicitBounds renders the declared upper bounds. The field is
// explicit_bounds in the schema, which is why the snapshot calls it Bounds
// rather than Buckets.
func otlpExplicitBounds(bounds []float64) []otlp.Double {
	//: a histogram with no declared bound is one +Inf bucket, and an empty
	//: repeated field is omitted.
	if len(bounds) == 0 {
		//: omitted by the omitempty tag.
		return nil
	}
	//: exactly-sized copy in ascending order.
	ladder := make([]otlp.Double, len(bounds))
	//: every bound is finite here — checkOTLPBucketLayout ran first.
	for i, bound := range bounds {
		//: only the JSON rendering differs.
		ladder[i] = otlp.Double(bound)
	}
	//: hand back the rendered ladder.
	return ladder
}

// newOTLPJSONExporter is the shared constructor.
func newOTLPJSONExporter(name coremetrics.ExporterName, dst io.Writer) *otlpJSONExporter {
	//: a writer-bound exporter whose only state is the shared stream's lock.
	return &otlpJSONExporter{name: name, stream: otlp.NewStream(dst, &otlpWriteFailure)}
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
	//: one terminated document, one write, under this signal's EXPORT_FAILED.
	return e.stream.Emit(doc)
}
