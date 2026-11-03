package metrics

import (
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
)

// ExponentialHistogram returns the exponential histogram series identified
// by name and attrs, satisfying core/observe/metrics.ExponentialMeter: created
// once with maxSize buckets per range — DefaultExponentialMaxSize for zero or
// less, InvalidBuckets for one — at ExponentialMaxScale, and found again on
// every later fetch, whose maxSize is ignored. The lookup is the other
// instruments': a read lock, a stack key, no allocation once the series
// exists.
func (m *memMeter) ExponentialHistogram(name string, maxSize int, attrs ...coreotel.AttrValue) coremetrics.Histogram {
	var sortBuf [maxStackAttrs]coreotel.AttrValue
	var keyBuf [seriesKeyCap]byte
	sorted := sortAttrs(sortBuf[:0], attrs)
	coremetrics.ValidateAttrs(sorted)
	key := appendSeriesKey(keyBuf[:0], kindExponentialHistogram, name, sorted)

	//: the series exists: no lock beyond the read.
	if inst, ok := m.exponentials.lookup(key); ok {
		return inst
	}
	build := func() *memExpHistogram { return newExpHistogram(maxSize) }
	//: created once, under the cardinality bound.
	return m.exponentials.admit(name, kindExponentialHistogram, key, sorted, build)
}

// collectExponentials gathers every exponential histogram series under its
// name, sorted by attribute set; nil when the meter holds none.
func collectExponentials(
	store map[string]*seriesEntry[*memExpHistogram], names map[string]*nameState,
	notes annotations, temporality coremetrics.Temporality, delta bool,
) map[string]coremetrics.ExponentialHistogramMetricValue {
	//: a meter with no exponential histogram publishes no map.
	if len(store) == 0 {
		return nil
	}
	out := make(map[string]coremetrics.ExponentialHistogramMetricValue, sizeGroups(names, len(store)))
	//: each name's envelope, its window carved to size.
	for name, slot := range carve[coremetrics.ExponentialHistogramValue](len(store), names, groupExponential) {
		out[name] = coremetrics.ExponentialHistogramMetricValue{
			Temporality: temporality,
			Description: notes.descriptions[name],
			Unit:        notes.units[name],
			Points:      slot.window,
		}
	}
	//: each series into its name's window.
	for _, entry := range store {
		point := entry.inst.snapshot(delta)
		point.Attrs = entry.attrs
		metric := out[entry.name]
		metric.Points = append(metric.Points, point)
		out[entry.name] = metric
	}
	//: sorted by attribute set, as every other point.
	for _, metric := range out {
		sortPoints(metric.Points, exponentialAttrs)
	}
	//: the map.
	return out
}

// exponentialAttrs is the sort key of an exponential point.
func exponentialAttrs(p coremetrics.ExponentialHistogramValue) []coreotel.AttrValue {
	//: its attribute set.
	return p.Attrs
}
