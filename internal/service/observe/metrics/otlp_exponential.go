package metrics

import (
	"encoding/hex"
	"strconv"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// otlpExemplar is metrics.proto's Exemplar, in field-number order:
// timeUnixNano (2), asDouble (3), spanId (4), traceId (5), asInt (6) — the
// identifiers in hex, as OTLP/JSON spells every trace identifier (ADR 0051
// §Decision 6). filtered_attributes (7) is never set: see
// coremetrics.ExemplarValue.
type otlpExemplar struct {
	TimeUnixNano otlp.Uint64  `json:"timeUnixNano"`
	AsDouble     *otlp.Double `json:"asDouble,omitempty"`
	SpanID       string       `json:"spanId"`
	TraceID      string       `json:"traceId"`
	AsInt        *otlp.Int64  `json:"asInt,omitempty"`
}

// otlpExponentialHistogram is metrics.proto's ExponentialHistogram: its
// points (1) and their temporality (2).
type otlpExponentialHistogram struct {
	DataPoints             []otlpExponentialHistogramDataPoint `json:"dataPoints,omitempty"`
	AggregationTemporality int                                 `json:"aggregationTemporality"`
}

// otlpExponentialHistogramDataPoint is ExponentialHistogramDataPoint in the
// order this encoder writes every point — the window, the totals, the
// layout, the exemplars, the attributes —: startTimeUnixNano (2),
// timeUnixNano (3), count (4), sum (5), scale (6), zeroCount (7), positive
// (8), negative (9), zeroThreshold (14, omitted at 0), exemplars (11),
// attributes (1).
type otlpExponentialHistogramDataPoint struct {
	StartTimeUnixNano otlp.Uint64     `json:"startTimeUnixNano"`
	TimeUnixNano      otlp.Uint64     `json:"timeUnixNano"`
	Count             otlp.Uint64     `json:"count"`
	Sum               *otlp.Double    `json:"sum,omitempty"`
	Scale             int32           `json:"scale"`
	ZeroCount         otlp.Uint64     `json:"zeroCount"`
	Positive          otlpBuckets     `json:"positive"`
	Negative          otlpBuckets     `json:"negative"`
	ZeroThreshold     float64         `json:"zeroThreshold,omitempty"`
	Exemplars         []otlpExemplar  `json:"exemplars,omitempty"`
	Attributes        []otlp.KeyValue `json:"attributes,omitempty"`
}

// otlpBuckets is ExponentialHistogramDataPoint.Buckets: offset (1), a sint32
// written as a JSON number, and bucketCounts (2), uint64 decimal strings.
type otlpBuckets struct {
	Offset       int32         `json:"offset"`
	BucketCounts []otlp.Uint64 `json:"bucketCounts,omitempty"`
}

// size is the number of buckets the run carries.
func (b *otlpBuckets) size() int {
	//: one per count.
	return len(b.BucketCounts)
}

// appendOTLPExponentials renders each exponential histogram name as one
// Metric carrying an ExponentialHistogram message, refusing an unresolved
// temporality as every other family does.
func appendOTLPExponentials(list []otlpMetric, metrics map[string]coremetrics.ExponentialHistogramMetricValue, start, end otlp.Uint64, arena *otlp.Arena) (extended []otlpMetric, err error) {
	//: in ascending name order, as every other family.
	for _, name := range sortedKeys(metrics) {
		metric := metrics[name]
		temporality, tempErr := otlpTemporality(name, metric.Temporality)
		//: a temporality the schema cannot carry aborts the document.
		if tempErr != nil {
			return nil, tempErr
		}
		list = append(list, otlpMetric{Name: name, Description: metric.Description, Unit: metric.Unit, ExponentialHistogram: &otlpExponentialHistogram{
			DataPoints:             otlpExponentialPoints(metric.Points, start, end, arena),
			AggregationTemporality: temporality,
		}})
	}
	//: the list, extended.
	return list, nil
}

// otlpExponentialPoints renders one exponential name's series, the runs
// copied as decimal strings.
func otlpExponentialPoints(series []coremetrics.ExponentialHistogramValue, start, end otlp.Uint64, arena *otlp.Arena) []otlpExponentialHistogramDataPoint {
	points := make([]otlpExponentialHistogramDataPoint, 0, len(series))
	//: each series, in the order Collect sorted them.
	for _, point := range series {
		points = append(points, otlpExponentialHistogramDataPoint{
			StartTimeUnixNano: start,
			TimeUnixNano:      end,
			Count:             otlp.Uint64(point.Count),
			Sum:               arena.Double(point.Sum),
			Scale:             point.Scale,
			ZeroCount:         otlp.Uint64(point.ZeroCount),
			Positive:          otlpBuckets{Offset: point.Positive.Offset, BucketCounts: otlpBucketCounts(point.Positive.Counts)},
			Negative:          otlpBuckets{Offset: point.Negative.Offset, BucketCounts: otlpBucketCounts(point.Negative.Counts)},
			ZeroThreshold:     point.ZeroThreshold,
			Exemplars:         otlpExemplars(point.Exemplars, false),
			Attributes:        arena.Attrs(point.Attrs),
		})
	}
	//: the rendered points.
	return points
}

// otlpOptionalSum is a histogram point's sum, nil — absent on the wire — for
// a source that keeps no total.
func otlpOptionalSum(sum float64, noSum bool, arena *otlp.Arena) *otlp.Double {
	//: nothing to state.
	if noSum {
		return nil
	}
	//: the total.
	return arena.Double(sum)
}

// otlpExemplars renders a point's exemplars, the value as asInt on a sum
// (integer) point and asDouble on a histogram one; nil for none.
func otlpExemplars(exemplars []coremetrics.ExemplarValue, integer bool) []otlpExemplar {
	//: no exemplar, no field.
	if len(exemplars) == 0 {
		return nil
	}
	out := make([]otlpExemplar, 0, len(exemplars))
	//: each, in the point's order.
	for _, ex := range exemplars {
		rendered := otlpExemplar{
			TimeUnixNano: otlp.UnixNano(ex.Time),
			SpanID:       hex.EncodeToString(ex.SpanID[:]),
			TraceID:      hex.EncodeToString(ex.TraceID[:]),
		}
		//: the oneof member the point's value type names.
		if integer {
			rendered.AsInt = new(otlp.Int64(int64(ex.Value)))
		} else {
			rendered.AsDouble = new(otlp.Double(ex.Value))
		}
		out = append(out, rendered)
	}
	//: the rendered list.
	return out
}

// appendExemplars appends "exemplars":[…], nothing for none.
func appendExemplars(dst []byte, exemplars []otlpExemplar) []byte {
	//: omitted when none was kept.
	if len(exemplars) == 0 {
		return dst
	}
	dst = otlp.AppendKey(dst, "exemplars")
	dst = append(dst, '[')
	//: each in field-number order.
	for i := range exemplars {
		//: comma-separated.
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = exemplars[i].appendJSON(dst)
	}
	//: closed.
	return append(dst, ']')
}

// appendJSON appends one Exemplar, in field-number order.
func (e *otlpExemplar) appendJSON(dst []byte) []byte {
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "timeUnixNano")
	dst = e.TimeUnixNano.AppendJSON(dst)
	//: asDouble (3) on a histogram's exemplar.
	if e.AsDouble != nil {
		dst = otlp.AppendKey(dst, "asDouble")
		dst = e.AsDouble.AppendJSON(dst)
	}
	dst = otlp.AppendKey(dst, "spanId")
	dst = otlp.AppendString(dst, e.SpanID)
	dst = otlp.AppendKey(dst, "traceId")
	dst = otlp.AppendString(dst, e.TraceID)
	//: asInt (6) on a sum's exemplar.
	if e.AsInt != nil {
		dst = otlp.AppendKey(dst, "asInt")
		dst = e.AsInt.AppendJSON(dst)
	}
	//: closed.
	return append(dst, '}')
}

// appendJSON appends one ExponentialHistogram: its points, then its
// temporality.
func (h *otlpExponentialHistogram) appendJSON(dst []byte) []byte {
	dst = append(dst, '{')
	//: dataPoints (1), omitted when empty.
	if len(h.DataPoints) > 0 {
		dst = otlp.AppendKey(dst, "dataPoints")
		dst = append(dst, '[')
		//: each point.
		for i := range h.DataPoints {
			//: comma-separated.
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = h.DataPoints[i].appendJSON(dst)
		}
		dst = append(dst, ']')
	}
	dst = otlp.AppendKey(dst, "aggregationTemporality")
	dst = strconv.AppendInt(dst, int64(h.AggregationTemporality), otlpDocDecimalBase)
	//: closed.
	return append(dst, '}')
}

// appendJSON appends one ExponentialHistogramDataPoint in the order its type
// documents; an empty run and a zero threshold are omitted.
func (p *otlpExponentialHistogramDataPoint) appendJSON(dst []byte) []byte {
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "startTimeUnixNano")
	dst = p.StartTimeUnixNano.AppendJSON(dst)
	dst = otlp.AppendKey(dst, "timeUnixNano")
	dst = p.TimeUnixNano.AppendJSON(dst)
	dst = otlp.AppendKey(dst, "count")
	dst = p.Count.AppendJSON(dst)
	//: sum (5), always known for an exponential histogram.
	if p.Sum != nil {
		dst = otlp.AppendKey(dst, "sum")
		dst = p.Sum.AppendJSON(dst)
	}
	dst = otlp.AppendKey(dst, "scale")
	dst = strconv.AppendInt(dst, int64(p.Scale), otlpDocDecimalBase)
	dst = otlp.AppendKey(dst, "zeroCount")
	dst = p.ZeroCount.AppendJSON(dst)
	dst = appendBuckets(dst, "positive", &p.Positive)
	dst = appendBuckets(dst, "negative", &p.Negative)
	//: zeroThreshold (14), the proto default 0 omitted.
	if p.ZeroThreshold != 0 {
		dst = otlp.AppendKey(dst, "zeroThreshold")
		dst = otlp.Double(p.ZeroThreshold).AppendJSON(dst)
	}
	dst = appendExemplars(dst, p.Exemplars)
	//: attributes (1), omitted when empty.
	if len(p.Attributes) > 0 {
		dst = otlp.AppendKey(dst, "attributes")
		dst = otlp.AppendKeyValues(dst, p.Attributes)
	}
	//: closed.
	return append(dst, '}')
}

// appendBuckets appends "key":{"offset":n,"bucketCounts":[…]}, nothing for
// an empty run — the proto's default message.
func appendBuckets(dst []byte, key string, b *otlpBuckets) []byte {
	//: an empty run is absent.
	if len(b.BucketCounts) == 0 {
		return dst
	}
	dst = otlp.AppendKey(dst, key)
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "offset")
	dst = strconv.AppendInt(dst, int64(b.Offset), otlpDocDecimalBase)
	dst = appendBucketCounts(dst, b.BucketCounts)
	//: closed.
	return append(dst, '}')
}
