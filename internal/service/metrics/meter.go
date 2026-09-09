// Package metrics — the in-memory Meter (a core/metrics.Meter).
package metrics

import (
	"slices"
	"sync"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// minSortableSeries is the shortest slice worth handing to a sort.
const minSortableSeries int = 2

// memMeter is the in-memory Meter: one seriesStore per instrument kind, guarded
// by an RWMutex. A fetch of an EXISTING series takes only the read lock; the
// write lock is held for creation alone. The instruments themselves are
// lock-free. Satisfies core Meter.
type memMeter struct {
	// maxSeries is the per-name cardinality bound, already clamped positive.
	maxSeries int

	mu         sync.RWMutex
	counters   seriesStore[*memCounter]
	gauges     seriesStore[*memGauge]
	histograms seriesStore[*memHistogram]
	names      map[string]*nameState
}

// NewMeter returns a fresh in-memory Meter bounded at
// DefaultMaxSeriesPerInstrument. Use NewMeterWithConfig to choose the bound.
func NewMeter() coremetrics.Meter {
	//: the zero config is the defaulted config, never the unbounded one.
	return NewMeterWithConfig(MeterConfig{})
}

// NewMeterWithConfig returns a fresh in-memory Meter honouring cfg. A
// non-positive MaxSeriesPerInstrument clamps to
// DefaultMaxSeriesPerInstrument — see MeterConfig for why it is not
// "unbounded".
func NewMeterWithConfig(cfg MeterConfig) coremetrics.Meter {
	//: clamp the bound before anything can observe it.
	maxSeries := cfg.MaxSeriesPerInstrument
	//: a zero or negative bound is an unset knob, not a request for infinity.
	if maxSeries <= 0 {
		//: the documented working default.
		maxSeries = DefaultMaxSeriesPerInstrument
	}
	//: start with an empty store per kind.
	return &memMeter{
		maxSeries:  maxSeries,
		counters:   newSeriesStore[*memCounter](kindCounter),
		gauges:     newSeriesStore[*memGauge](kindGauge),
		histograms: newSeriesStore[*memHistogram](kindHistogram),
		names:      make(map[string]*nameState, 0),
	}
}

// Counter returns the counter series identified by name + labels, creating it
// once. A name already bound to a different instrument kind panics with
// InstrumentKindConflict; an unusable label set panics with InvalidLabel.
func (m *memMeter) Counter(name string, labels ...coremetrics.LabelValue) coremetrics.Counter {
	//: stack scratch — the sorted set and the encoded key never reach the heap
	//: on the hot path, which is what keeps a labelled fetch allocation-free.
	var sortBuf [maxStackLabels]coremetrics.LabelValue
	var keyBuf [seriesKeyCap]byte
	sorted := sortLabels(sortBuf[:0], labels)
	validateLabels(sorted)
	key := appendSeriesKey(keyBuf[:0], name, sorted)

	//: the overwhelmingly common case: the series already exists.
	if inst, ok := m.counters.lookup(m, key); ok {
		//: idempotent fetch.
		return inst
	}
	//: first sighting — take the write lock and create or overflow.
	return m.counters.admit(m, name, key, sorted, newMemCounter)
}

// Gauge returns the gauge series identified by name + labels, creating it once.
func (m *memMeter) Gauge(name string, labels ...coremetrics.LabelValue) coremetrics.Gauge {
	//: same stack-scratch discipline as Counter.
	var sortBuf [maxStackLabels]coremetrics.LabelValue
	var keyBuf [seriesKeyCap]byte
	sorted := sortLabels(sortBuf[:0], labels)
	validateLabels(sorted)
	key := appendSeriesKey(keyBuf[:0], name, sorted)

	//: existing series short-circuits under the read lock.
	if inst, ok := m.gauges.lookup(m, key); ok {
		//: idempotent fetch.
		return inst
	}
	//: create under the write lock.
	return m.gauges.admit(m, name, key, sorted, newMemGauge)
}

// Histogram returns the histogram series identified by name + labels, creating
// it once with buckets. The buckets are fixed at creation and ignored on every
// later fetch of the same series — counts already recorded are positional, so
// re-bucketing would attach every stored count to a boundary it was never
// measured against.
func (m *memMeter) Histogram(name string, buckets []float64, labels ...coremetrics.LabelValue) coremetrics.Histogram {
	//: same stack-scratch discipline as Counter.
	var sortBuf [maxStackLabels]coremetrics.LabelValue
	var keyBuf [seriesKeyCap]byte
	sorted := sortLabels(sortBuf[:0], labels)
	validateLabels(sorted)
	key := appendSeriesKey(keyBuf[:0], name, sorted)

	//: existing series short-circuits under the read lock.
	if inst, ok := m.histograms.lookup(m, key); ok {
		//: idempotent fetch (buckets fixed at first creation).
		return inst
	}
	//: the buckets ride into the slow path in a closure — creation only.
	build := func() *memHistogram { return newHistogram(buckets) }
	//: create under the write lock.
	return m.histograms.admit(m, name, key, sorted, build)
}

// Collect copies every series into a SnapshotValue. Read-only — holds RLock.
func (m *memMeter) Collect() coremetrics.SnapshotValue {
	m.mu.RLock()
	defer m.mu.RUnlock()
	//: group each kind by instrument name; empty maps, never nil.
	return coremetrics.SnapshotValue{
		Counters:   collectCounters(m.counters.byKey, m.names),
		Gauges:     collectGauges(m.gauges.byKey, m.names),
		Histograms: collectHistograms(m.histograms.byKey, m.names),
	}
}

// collectCounters groups every counter series under its instrument name.
func collectCounters(
	store map[string]*seriesEntry[*memCounter], names map[string]*nameState,
) map[string][]coremetrics.CounterValue {
	//: one arena for every counter series, carved per name.
	out := layout[coremetrics.CounterValue](len(store), names, kindCounter)
	//: snapshot each series' cumulative total.
	for _, entry := range store {
		//: labels alias the meter's copy — documented on CounterValue.
		out[entry.name] = append(out[entry.name], coremetrics.CounterValue{
			Labels: entry.labels,
			Value:  entry.inst.load(),
		})
	}
	//: deterministic order per name.
	sortSeries(out, counterLabels)
	//: hand back the grouped copy.
	return out
}

// collectGauges groups every gauge series under its instrument name.
func collectGauges(
	store map[string]*seriesEntry[*memGauge], names map[string]*nameState,
) map[string][]coremetrics.GaugeValue {
	//: same grouping as counters.
	out := layout[coremetrics.GaugeValue](len(store), names, kindGauge)
	//: snapshot each series' instantaneous reading.
	for _, entry := range store {
		//: labels alias the meter's copy.
		out[entry.name] = append(out[entry.name], coremetrics.GaugeValue{
			Labels: entry.labels,
			Value:  entry.inst.load(),
		})
	}
	//: deterministic order per name.
	sortSeries(out, gaugeLabels)
	//: hand back the grouped copy.
	return out
}

// collectHistograms groups every histogram series under its instrument name.
func collectHistograms(
	store map[string]*seriesEntry[*memHistogram], names map[string]*nameState,
) map[string][]coremetrics.HistogramValue {
	//: same grouping as counters.
	out := layout[coremetrics.HistogramValue](len(store), names, kindHistogram)
	//: snapshot each series' bucketed distribution.
	for _, entry := range store {
		//: the histogram's own snapshot fills everything but the labels.
		value := entry.inst.snapshot()
		value.Labels = entry.labels
		out[entry.name] = append(out[entry.name], value)
	}
	//: deterministic order per name.
	sortSeries(out, histogramLabels)
	//: hand back the grouped copy.
	return out
}

// layout carves ONE backing array into an empty, exactly-sized window per
// instrument name, ready to be appended into.
//
// The alternative — letting each name's slice allocate and grow on its own —
// makes a collection cost one allocation per instrument NAME plus a doubling
// copy per name that carries many series. Both scale with exactly the axis this
// feature bounds, on the path a scraper walks every few seconds. One arena
// makes it a constant.
//
// The per-name sizes come from the meter's own bookkeeping rather than a
// counting pass over the store: names already knows how many series each name
// holds, and summed over the names of one kind that is exactly storeLen.
func layout[V any](storeLen int, names map[string]*nameState, kind instrumentKind) map[string][]V {
	//: one allocation for every series of this kind.
	arena := make([]V, 0, storeLen)
	//: a kind cannot have more distinct names than it has series, and names
	//: spans every kind — sizing on the smaller of the two keeps a meter with
	//: a thousand counters from pre-sizing an empty gauge map for a thousand.
	out := make(map[string][]V, min(len(names), storeLen))
	offset := 0
	//: carve a zero-length, capped window per name of this kind.
	for name, state := range names {
		//: a name belongs to exactly one kind.
		if state.kind != kind {
			//: another kind's arena will carve this one.
			continue
		}
		size := state.seriesCap()
		//: the sizes sum to storeLen; the guard turns a broken invariant into
		//: a slower collection rather than a panic in the scrape path.
		if offset+size > cap(arena) {
			//: stand-alone window.
			out[name] = make([]V, 0, size)
			continue
		}
		//: capping at offset+size stops one name's appends from spilling into
		//: the next name's window.
		out[name] = arena[offset : offset : offset+size]
		offset += size
	}
	//: hand back the empty windows.
	return out
}

// sortSeries orders each name's series by label set so a snapshot renders
// identically twice in a row given the same values.
func sortSeries[V any](groups map[string][]V, labelsOf func(V) []coremetrics.LabelValue) {
	//: every name's slice, independently.
	for _, series := range groups {
		//: a single-series name — the overwhelming majority — is already
		//: ordered, and the call would otherwise cost a func value per name.
		if len(series) < minSortableSeries {
			//: nothing to order.
			continue
		}
		//: lexicographic on the sorted label sets.
		slices.SortFunc(series, func(a, b V) int {
			//: compare the identities, never the values.
			return compareLabels(labelsOf(a), labelsOf(b))
		})
	}
}

// counterLabels projects a counter series' label set.
func counterLabels(v coremetrics.CounterValue) []coremetrics.LabelValue {
	//: identity only.
	return v.Labels
}

// gaugeLabels projects a gauge series' label set.
func gaugeLabels(v coremetrics.GaugeValue) []coremetrics.LabelValue {
	//: identity only.
	return v.Labels
}

// histogramLabels projects a histogram series' label set.
func histogramLabels(v coremetrics.HistogramValue) []coremetrics.LabelValue {
	//: identity only.
	return v.Labels
}
