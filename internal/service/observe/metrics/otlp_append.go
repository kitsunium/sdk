package metrics

import (
	"strconv"

	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// The capacity an OTLP/JSON document starts with, so the appends below grow it
// a few times rather than from nothing: the envelope, plus an estimate per data
// point and per histogram bucket. An estimate, never a bound — append grows the
// buffer past it.
const (
	otlpDocEnvelopeBytes int = 256
	otlpDocPointBytes    int = 320
	otlpDocBucketBytes   int = 8
	otlpDocDecimalBase   int = 10
)

// appendJSON appends the request to dst, byte for byte what otlp.Marshal
// renders for it — member order, omissions, a nil slice that is not omitted
// spelled null — without reflection and without a slice per scalar.
func (r *otlpRequest) appendJSON(dst []byte) []byte {
	//: resourceMetrics, never omitted.
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "resourceMetrics")
	//: a nil list is null, as encoding/json spells it.
	if r.ResourceMetrics == nil {
		//: the literal.
		dst = otlp.AppendNull(dst)
		return append(dst, '}')
	}
	//: one entry per Resource.
	dst = append(dst, '[')
	for i := range r.ResourceMetrics {
		//: comma-separated.
		if i > 0 {
			//: the separator.
			dst = append(dst, ',')
		}
		dst = r.ResourceMetrics[i].appendJSON(dst)
	}
	return append(dst, ']', '}')
}

// appendJSON appends one ResourceMetrics.
func (r *otlpResourceMetrics) appendJSON(dst []byte) []byte {
	//: resource (1), then scopeMetrics (2), never omitted.
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "resource")
	dst = r.Resource.AppendJSON(dst)
	dst = otlp.AppendKey(dst, "scopeMetrics")
	//: a nil list is null.
	if r.ScopeMetrics == nil {
		//: the literal.
		dst = otlp.AppendNull(dst)
		return append(dst, '}')
	}
	//: one entry per Scope.
	dst = append(dst, '[')
	for i := range r.ScopeMetrics {
		//: comma-separated.
		if i > 0 {
			//: the separator.
			dst = append(dst, ',')
		}
		dst = r.ScopeMetrics[i].appendJSON(dst)
	}
	return append(dst, ']', '}')
}

// appendJSON appends one ScopeMetrics, its metrics omitted when empty.
func (s *otlpScopeMetrics) appendJSON(dst []byte) []byte {
	//: scope (1).
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "scope")
	dst = s.Scope.AppendJSON(dst)
	//: metrics (2), omitted when empty.
	if len(s.Metrics) > 0 {
		//: the flat list.
		dst = otlp.AppendKey(dst, "metrics")
		dst = append(dst, '[')
		for i := range s.Metrics {
			//: comma-separated.
			if i > 0 {
				//: the separator.
				dst = append(dst, ',')
			}
			dst = s.Metrics[i].appendJSON(dst)
		}
		dst = append(dst, ']')
	}
	return append(dst, '}')
}

// appendJSON appends one Metric: its name, its description when it has one,
// and the one data case that is set.
func (m *otlpMetric) appendJSON(dst []byte) []byte {
	//: name (1), never omitted.
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "name")
	dst = otlp.AppendString(dst, m.Name)
	//: description (2), omitted when empty.
	if m.Description != "" {
		//: escaped like any string.
		dst = otlp.AppendKey(dst, "description")
		dst = otlp.AppendString(dst, m.Description)
	}
	//: gauge (5).
	if m.Gauge != nil {
		//: its points only.
		dst = otlp.AppendKey(dst, "gauge")
		dst = append(dst, '{')
		dst = appendNumberPoints(dst, m.Gauge.DataPoints)
		dst = append(dst, '}')
	}
	//: sum (7).
	if m.Sum != nil {
		//: points, temporality, monotonicity.
		dst = otlp.AppendKey(dst, "sum")
		dst = m.Sum.appendJSON(dst)
	}
	//: histogram (9).
	if m.Histogram != nil {
		//: points, temporality.
		dst = otlp.AppendKey(dst, "histogram")
		dst = m.Histogram.appendJSON(dst)
	}
	return append(dst, '}')
}

// appendJSON appends one Sum; isMonotonic is always emitted.
func (s *otlpSum) appendJSON(dst []byte) []byte {
	//: dataPoints (1), omitted when empty.
	dst = append(dst, '{')
	dst = appendNumberPoints(dst, s.DataPoints)
	//: aggregationTemporality (2), an enum's integer.
	dst = otlp.AppendKey(dst, "aggregationTemporality")
	dst = strconv.AppendInt(dst, int64(s.AggregationTemporality), otlpDocDecimalBase)
	//: isMonotonic (3), always emitted.
	dst = otlp.AppendKey(dst, "isMonotonic")
	dst = otlp.AppendBool(dst, s.IsMonotonic)
	return append(dst, '}')
}

// appendJSON appends one Histogram.
func (h *otlpHistogram) appendJSON(dst []byte) []byte {
	//: dataPoints (1), omitted when empty.
	dst = append(dst, '{')
	//: when present.
	if len(h.DataPoints) > 0 {
		//: one HistogramDataPoint each.
		dst = otlp.AppendKey(dst, "dataPoints")
		dst = append(dst, '[')
		for i := range h.DataPoints {
			//: comma-separated.
			if i > 0 {
				//: the separator.
				dst = append(dst, ',')
			}
			dst = h.DataPoints[i].appendJSON(dst)
		}
		dst = append(dst, ']')
	}
	//: aggregationTemporality (2).
	dst = otlp.AppendKey(dst, "aggregationTemporality")
	dst = strconv.AppendInt(dst, int64(h.AggregationTemporality), otlpDocDecimalBase)
	return append(dst, '}')
}

// appendNumberPoints appends the dataPoints member of a Gauge or a Sum, or
// nothing when there are none — the member is omitted when empty.
func appendNumberPoints(dst []byte, points []otlpNumberDataPoint) []byte {
	//: an empty list is omitted.
	if len(points) == 0 {
		//: nothing written.
		return dst
	}
	//: one NumberDataPoint each.
	dst = otlp.AppendKey(dst, "dataPoints")
	dst = append(dst, '[')
	for i := range points {
		//: comma-separated.
		if i > 0 {
			//: the separator.
			dst = append(dst, ',')
		}
		dst = points[i].appendJSON(dst)
	}
	return append(dst, ']')
}

// appendJSON appends one NumberDataPoint, in field-number order.
func (p *otlpNumberDataPoint) appendJSON(dst []byte) []byte {
	//: the window, both decimal strings.
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "startTimeUnixNano")
	dst = p.StartTimeUnixNano.AppendJSON(dst)
	dst = otlp.AppendKey(dst, "timeUnixNano")
	dst = p.TimeUnixNano.AppendJSON(dst)
	//: asDouble (4), the oneof member a gauge sets.
	if p.AsDouble != nil {
		//: a number or a name.
		dst = otlp.AppendKey(dst, "asDouble")
		dst = p.AsDouble.AppendJSON(dst)
	}
	//: asInt (6), the oneof member a sum sets.
	if p.AsInt != nil {
		//: a decimal string.
		dst = otlp.AppendKey(dst, "asInt")
		dst = p.AsInt.AppendJSON(dst)
	}
	//: attributes (7), omitted when empty.
	if len(p.Attributes) > 0 {
		//: the repeated KeyValue.
		dst = otlp.AppendKey(dst, "attributes")
		dst = otlp.AppendKeyValues(dst, p.Attributes)
	}
	return append(dst, '}')
}

// appendJSON appends one HistogramDataPoint, in field-number order; sum is
// never omitted, and a nil one is null.
func (p *otlpHistogramDataPoint) appendJSON(dst []byte) []byte {
	//: the window and the count, decimal strings.
	dst = append(dst, '{')
	dst = otlp.AppendKey(dst, "startTimeUnixNano")
	dst = p.StartTimeUnixNano.AppendJSON(dst)
	dst = otlp.AppendKey(dst, "timeUnixNano")
	dst = p.TimeUnixNano.AppendJSON(dst)
	dst = otlp.AppendKey(dst, "count")
	dst = p.Count.AppendJSON(dst)
	//: sum (5), never omitted.
	dst = otlp.AppendKey(dst, "sum")
	//: a nil pointer is null, as encoding/json spells it.
	if p.Sum == nil {
		//: the literal.
		dst = otlp.AppendNull(dst)
	} else {
		//: a number or a name.
		dst = p.Sum.AppendJSON(dst)
	}
	//: bucketCounts (6) and explicitBounds (7), each omitted when empty.
	dst = appendBucketCounts(dst, p.BucketCounts)
	dst = appendExplicitBounds(dst, p.ExplicitBounds)
	//: attributes (9), omitted when empty.
	if len(p.Attributes) > 0 {
		//: the repeated KeyValue.
		dst = otlp.AppendKey(dst, "attributes")
		dst = otlp.AppendKeyValues(dst, p.Attributes)
	}
	return append(dst, '}')
}

// appendBucketCounts appends the bucketCounts member — decimal strings — or
// nothing when the ladder is empty, the member being omitted then.
func appendBucketCounts(dst []byte, counts []otlp.Uint64) []byte {
	//: an empty ladder is omitted.
	if len(counts) == 0 {
		//: nothing written.
		return dst
	}
	//: one decimal string per bucket.
	dst = otlp.AppendKey(dst, "bucketCounts")
	dst = append(dst, '[')
	for i := range counts {
		//: comma-separated.
		if i > 0 {
			//: the separator.
			dst = append(dst, ',')
		}
		dst = counts[i].AppendJSON(dst)
	}
	return append(dst, ']')
}

// appendExplicitBounds appends the explicitBounds member — numbers — or
// nothing when there are no bounds, the member being omitted then.
func appendExplicitBounds(dst []byte, bounds []otlp.Double) []byte {
	//: no bounds are omitted.
	if len(bounds) == 0 {
		//: nothing written.
		return dst
	}
	//: one number per bound.
	dst = otlp.AppendKey(dst, "explicitBounds")
	dst = append(dst, '[')
	for i := range bounds {
		//: comma-separated.
		if i > 0 {
			//: the separator.
			dst = append(dst, ',')
		}
		dst = bounds[i].AppendJSON(dst)
	}
	return append(dst, ']')
}

// otlpDocCapacity estimates the size of the document list renders into, so the
// buffer starts near it.
func otlpDocCapacity(list []otlpMetric) int {
	//: the envelope, then each point and each histogram bucket.
	size := otlpDocEnvelopeBytes
	for i := range list {
		//: one case is set per metric.
		switch metric := &list[i]; {
		//: a gauge's points.
		case metric.Gauge != nil:
			//: one estimate per point.
			size += len(metric.Gauge.DataPoints) * otlpDocPointBytes
		//: a sum's points.
		case metric.Sum != nil:
			//: one estimate per point.
			size += len(metric.Sum.DataPoints) * otlpDocPointBytes
		//: a histogram's points, which carry a ladder each.
		case metric.Histogram != nil:
			//: one estimate per point and per bucket.
			for j := range metric.Histogram.DataPoints {
				//: the point and its two arrays.
				point := &metric.Histogram.DataPoints[j]
				size += otlpDocPointBytes + (len(point.BucketCounts)+len(point.ExplicitBounds))*otlpDocBucketBytes
			}
		}
	}
	return size
}
