// Package metrics — the in-memory Meter (a core/metrics.FullMeter).
package metrics

import (
	"slices"
	"sync"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// minSortableSeries is the shortest slice worth handing to a sort.
const minSortableSeries int = 2

// memMeter is the in-memory Meter: one seriesStore per snapshot group, guarded
// by an RWMutex. A fetch of an EXISTING series takes only the read lock; the
// write lock is held for creation alone. The instruments themselves are
// lock-free. Satisfies core FullMeter.
type memMeter struct {
	// maxSeries is the per-name cardinality bound, already clamped positive.
	maxSeries int
	// temporality is already resolved — never TemporalityUnspecified.
	temporality coremetrics.Temporality
	// resource and scope are normalised once and shared by every snapshot;
	// neither is mutated after construction.
	resource coremetrics.ResourceValue
	scope    coremetrics.ScopeValue
	clk      clock.Clock

	// collectMu serialises Collect against itself. Collect is a MUTATION
	// under delta temporality (it consumes the window it reports) and it is
	// one under any temporality once an observable is registered (a callback
	// writes the series it reports). Two concurrent collections would then
	// each carry away part of the data with no way to notice.
	collectMu sync.Mutex
	// startTime opens the window the next snapshot covers. Guarded by
	// collectMu; it advances only under delta temporality.
	startTime time.Time

	mu         sync.RWMutex
	sums       seriesStore[*memSum]
	gauges     seriesStore[*memGauge]
	histograms seriesStore[*memHistogram]
	names      map[string]*nameState
	// observers holds every registered asynchronous instrument, read once per
	// Collect. Guarded by mu.
	observers []observer
	// descriptions maps an instrument NAME to its docstring. Guarded by mu,
	// and deliberately NIL until the first Describe: a nil map reads as the
	// empty one, so a meter nobody documents allocates nothing here and
	// Collect still finds "" for every name. See meter_describe.go.
	//
	// It is a map of its own rather than a field on nameState because a
	// description is NON-IDENTIFYING (the OTel data model says so) and carries
	// no instrument kind, while every nameState does. Storing it there would
	// force Describe to invent a kind for a name that may never mint an
	// instrument.
	descriptions map[string]string
}

// NewMeter returns a fresh in-memory Meter with every MeterConfig knob at its
// resolved default: bounded at DefaultMaxSeriesPerInstrument, cumulative,
// service.name=unknown_service, this SDK as the scope, and the system clock.
func NewMeter() coremetrics.FullMeter {
	//: the zero config is the defaulted config, never the inert one.
	return newMemMeter(MeterConfig{})
}

// NewMeterWithConfig returns a fresh in-memory Meter honouring cfg. Every unset
// field resolves to a working value and none of them to an inert one — see
// MeterConfig for each knob's rule.
func NewMeterWithConfig(cfg MeterConfig) coremetrics.FullMeter {
	//: delegate; both exported constructors stay INLINABLE on purpose.
	return newMemMeter(cfg)
}

// newMemMeter builds the concrete meter. It exists so that the two exported
// constructors stay small enough to inline, which is not cosmetic: inlining is
// what lets a caller's `var m FullMeter = NewMeter()` carry the CONCRETE type
// to the call sites below it, and that is what lets the compiler devirtualise
// `m.Counter(name, attrs...)` and prove the variadic attribute slice does not
// escape. Behind an opaque interface it must assume it does, and every
// attributed observation costs one small heap allocation. Measured, and pinned
// by the allocation gate; see BENCH.md §Caveats.
func newMemMeter(cfg MeterConfig) *memMeter {
	//: clamp / refuse every knob before anything can observe one.
	resolved := cfg.resolve()
	//: the first window opens the moment the meter exists.
	meter := &memMeter{
		maxSeries:   resolved.MaxSeriesPerInstrument,
		temporality: resolved.Temporality,
		resource:    resolved.Resource,
		scope:       resolved.Scope,
		clk:         resolved.Clock,
		startTime:   resolved.Clock.Now(),
		names:       make(map[string]*nameState, 0),
	}
	//: each store keeps a back-pointer to the meter it belongs to, which is
	//: what keeps admit's signature short enough to stay allocation-free —
	//: see seriesStore.
	meter.sums = newSeriesStore[*memSum](meter)
	meter.gauges = newSeriesStore[*memGauge](meter)
	meter.histograms = newSeriesStore[*memHistogram](meter)
	//: hand back the wide view; every caller may narrow it to a Meter.
	return meter
}

// Counter returns the monotonic sum series identified by name + attrs, creating
// it once. A name already bound to a different instrument kind panics with
// InstrumentKindConflict; an unusable attribute set panics with
// InvalidAttribute.
func (m *memMeter) Counter(name string, attrs ...coremetrics.AttrValue) coremetrics.Counter {
	//: stack scratch — the sorted set and the encoded key never reach the heap
	//: on the hot path, which is what keeps an attributed fetch allocation-free.
	var sortBuf [maxStackAttrs]coremetrics.AttrValue
	var keyBuf [seriesKeyCap]byte
	sorted := sortAttrs(sortBuf[:0], attrs)
	coremetrics.ValidateAttrs(sorted)
	key := appendSeriesKey(keyBuf[:0], kindCounter, name, sorted)

	//: the overwhelmingly common case: the series already exists.
	if inst, ok := m.sums.lookup(key); ok {
		//: idempotent fetch.
		return inst
	}
	//: first sighting — take the write lock and create or overflow.
	return m.sums.admit(name, kindCounter, key, sorted, newCounter)
}

// UpDownCounter returns the non-monotonic sum series identified by name +
// attrs, creating it once. Fetching a Counter name as an UpDownCounter panics
// with InstrumentKindConflict — it would flip Monotonic on a metric a backend
// has already learned to read as never decreasing.
func (m *memMeter) UpDownCounter(name string, attrs ...coremetrics.AttrValue) coremetrics.UpDownCounter {
	//: same stack-scratch discipline as Counter.
	var sortBuf [maxStackAttrs]coremetrics.AttrValue
	var keyBuf [seriesKeyCap]byte
	sorted := sortAttrs(sortBuf[:0], attrs)
	coremetrics.ValidateAttrs(sorted)
	key := appendSeriesKey(keyBuf[:0], kindUpDownCounter, name, sorted)

	//: existing series short-circuits under the read lock.
	if inst, ok := m.sums.lookup(key); ok {
		//: idempotent fetch.
		return inst
	}
	//: create under the write lock.
	return m.sums.admit(name, kindUpDownCounter, key, sorted, newUpDownCounter)
}

// Gauge returns the gauge series identified by name + attrs, creating it once.
func (m *memMeter) Gauge(name string, attrs ...coremetrics.AttrValue) coremetrics.Gauge {
	//: same stack-scratch discipline as Counter.
	var sortBuf [maxStackAttrs]coremetrics.AttrValue
	var keyBuf [seriesKeyCap]byte
	sorted := sortAttrs(sortBuf[:0], attrs)
	coremetrics.ValidateAttrs(sorted)
	key := appendSeriesKey(keyBuf[:0], kindGauge, name, sorted)

	//: existing series short-circuits under the read lock.
	if inst, ok := m.gauges.lookup(key); ok {
		//: idempotent fetch.
		return inst
	}
	//: create under the write lock.
	return m.gauges.admit(name, kindGauge, key, sorted, newMemGauge)
}

// Histogram returns the histogram series identified by name + attrs, creating
// it once with buckets. The buckets are fixed at creation and ignored on every
// later fetch of the same series — counts already recorded are positional, so
// re-bucketing would attach every stored count to a boundary it was never
// measured against.
func (m *memMeter) Histogram(
	name string, buckets []float64, attrs ...coremetrics.AttrValue,
) coremetrics.Histogram {
	//: same stack-scratch discipline as Counter.
	var sortBuf [maxStackAttrs]coremetrics.AttrValue
	var keyBuf [seriesKeyCap]byte
	sorted := sortAttrs(sortBuf[:0], attrs)
	coremetrics.ValidateAttrs(sorted)
	key := appendSeriesKey(keyBuf[:0], kindHistogram, name, sorted)

	//: existing series short-circuits under the read lock.
	if inst, ok := m.histograms.lookup(key); ok {
		//: idempotent fetch (buckets fixed at first creation).
		return inst
	}
	//: the buckets ride into the slow path in a closure — creation only.
	build := func() *memHistogram { return newHistogram(buckets) }
	//: create under the write lock.
	return m.histograms.admit(name, kindHistogram, key, sorted, build)
}

// Collect copies every series into a SnapshotValue.
//
// It is serialised against itself by collectMu, and it is a MUTATION twice
// over: every registered observable callback is run to produce this window's
// values, and under delta temporality every synchronous accumulator is swapped
// to zero. The RWMutex still only guards the maps — the instruments themselves
// stay lock-free.
func (m *memMeter) Collect() coremetrics.SnapshotValue {
	m.collectMu.Lock()
	defer m.collectMu.Unlock()
	//: a delta reader consumes each window; a cumulative one keeps it.
	delta := m.temporality == coremetrics.TemporalityDelta
	//: read every asynchronous instrument BEFORE the snapshot lock, because
	//: a callback resolves series and would deadlock behind it.
	m.runObservers(delta)
	//: the window closes now, after every value has been produced.
	now := m.clk.Now()

	m.mu.RLock()
	//: group each kind by instrument name; empty maps, never nil.
	snap := coremetrics.SnapshotValue{
		Resource:   m.resource,
		Scope:      m.scope,
		StartTime:  m.startTime,
		Time:       now,
		Sums:       collectSums(m.sums.byKey, m.names, m.descriptions, m.temporality, delta),
		Gauges:     collectGauges(m.gauges.byKey, m.names, m.descriptions),
		Histograms: collectHistograms(m.histograms.byKey, m.names, m.descriptions, m.temporality, delta),
	}
	m.mu.RUnlock()

	//: a delta window is consumed, so the next one starts where this ended.
	//: a cumulative window keeps its original start forever, which is exactly
	//: what "repeat the starting timestamp" means in the OTel data model.
	if delta {
		//: advance.
		m.startTime = now
	}
	//: hand back the payload.
	return snap
}

// collectSums groups every sum series under its instrument name, carrying the
// meter's temporality and each name's monotonicity onto the metric.
func collectSums(
	store map[string]*seriesEntry[*memSum], names map[string]*nameState,
	descriptions map[string]string, temporality coremetrics.Temporality, delta bool,
) map[string]coremetrics.SumMetricValue {
	//: one arena for every sum series, carved per name.
	out := make(map[string]coremetrics.SumMetricValue, sizeGroups(names, len(store)))
	//: the two facts OTel puts on the METRIC rather than on the point.
	for name, slot := range carve[coremetrics.SumValue](len(store), names, groupSum) {
		//: monotonicity belongs to the name — every series under
		//: http_requests_total is a counter. So does the description, which is
		//: "" for an undescribed name and for every name when the map is nil.
		out[name] = coremetrics.SumMetricValue{
			Temporality: temporality,
			Monotonic:   slot.kind.monotonic(),
			Description: descriptions[name],
			Points:      slot.window,
		}
	}
	//: snapshot each series' total, consuming it when the reader is delta.
	for _, entry := range store {
		//: attrs alias the meter's copy — documented on SumValue.
		metric := out[entry.name]
		metric.Points = append(metric.Points, coremetrics.SumValue{
			Attrs: entry.attrs,
			Value: entry.inst.collect(delta),
		})
		out[entry.name] = metric
	}
	//: deterministic order per name.
	for _, metric := range out {
		//: the slice header is a copy, its backing array is not.
		sortPoints(metric.Points, sumAttrs)
	}
	//: hand back the grouped copy.
	return out
}

// collectGauges groups every gauge series under its instrument name. A gauge
// carries no temporality, so neither does this walk.
func collectGauges(
	store map[string]*seriesEntry[*memGauge], names map[string]*nameState,
	descriptions map[string]string,
) map[string]coremetrics.GaugeMetricValue {
	//: same grouping as sums.
	out := make(map[string]coremetrics.GaugeMetricValue, sizeGroups(names, len(store)))
	//: points and a docstring, exactly like OTLP's Gauge message — a sampled
	//: reading covers no window, so there is no temporality to carry.
	for name, slot := range carve[coremetrics.GaugeValue](len(store), names, groupGauge) {
		//: the envelope is the window.
		out[name] = coremetrics.GaugeMetricValue{
			Description: descriptions[name],
			Points:      slot.window,
		}
	}
	//: snapshot each series' instantaneous reading.
	for _, entry := range store {
		//: attrs alias the meter's copy.
		metric := out[entry.name]
		metric.Points = append(metric.Points, coremetrics.GaugeValue{
			Attrs: entry.attrs,
			Value: entry.inst.load(),
		})
		out[entry.name] = metric
	}
	//: deterministic order per name.
	for _, metric := range out {
		//: in-place on the shared backing array.
		sortPoints(metric.Points, gaugeAttrs)
	}
	//: hand back the grouped copy.
	return out
}

// collectHistograms groups every histogram series under its instrument name.
func collectHistograms(
	store map[string]*seriesEntry[*memHistogram], names map[string]*nameState,
	descriptions map[string]string, temporality coremetrics.Temporality, delta bool,
) map[string]coremetrics.HistogramMetricValue {
	//: same grouping as sums.
	out := make(map[string]coremetrics.HistogramMetricValue, sizeGroups(names, len(store)))
	//: a histogram has a temporality but no monotonicity.
	for name, slot := range carve[coremetrics.HistogramValue](len(store), names, groupHistogram) {
		//: the envelope carries the window it covers.
		out[name] = coremetrics.HistogramMetricValue{
			Temporality: temporality,
			Description: descriptions[name],
			Points:      slot.window,
		}
	}
	//: snapshot each series' bucketed distribution.
	for _, entry := range store {
		//: the histogram's own snapshot fills everything but the attributes.
		point := entry.inst.snapshot(delta)
		point.Attrs = entry.attrs
		metric := out[entry.name]
		metric.Points = append(metric.Points, point)
		out[entry.name] = metric
	}
	//: deterministic order per name.
	for _, metric := range out {
		//: in-place on the shared backing array.
		sortPoints(metric.Points, histogramAttrs)
	}
	//: hand back the grouped copy.
	return out
}

// carve yields one carved slot per instrument name of group, all cut out of a
// SINGLE backing array.
//
// The alternative — letting each name's slice allocate and grow on its own —
// makes a collection cost one allocation per instrument NAME plus a doubling
// copy per name that carries many series. Both scale with exactly the axis this
// feature bounds, on the path a scraper walks every few seconds. One arena
// makes it a constant.
//
// The per-name sizes come from the meter's own bookkeeping rather than a
// counting pass over the store: names already knows how many series each name
// holds, and summed over the names of one group that is exactly storeLen.
func carve[V any](
	storeLen int, names map[string]*nameState, group outputGroup,
) func(func(string, carved[V]) bool) {
	//: a range-over-func so the arena is built once, at the first iteration,
	//: and each caller wraps the window in its own metric type without a
	//: callback that would have to ignore half its parameters.
	return func(yield func(string, carved[V]) bool) {
		//: one allocation for every series of this group.
		arena := make([]V, 0, storeLen)
		offset := 0
		//: carve a zero-length, capped window per name of this group.
		for name, state := range names {
			//: a name belongs to exactly one group.
			if state.kind.group() != group {
				//: another group's arena will carve this one.
				continue
			}
			size := state.seriesCap()
			//: the sizes sum to storeLen; the guard turns a broken invariant
			//: into a slower collection rather than a panic in the scrape path.
			window := arena[offset : offset : offset+size]
			//: stand-alone window when the arena would overrun.
			if offset+size > cap(arena) {
				//: correctness before the allocation budget.
				window = make([]V, 0, size)
			} else {
				//: capping at offset+size stops one name's appends from
				//: spilling into the next name's window.
				offset += size
			}
			//: hand the slot to the collector.
			if !yield(name, carved[V]{kind: state.kind, window: window}) {
				//: the caller stopped early.
				return
			}
		}
	}
}

// sizeGroups is the map capacity a collection pre-sizes to. A group cannot have
// more distinct names than it has series, and names spans every group — sizing
// on the smaller of the two keeps a meter with a thousand counters from
// pre-sizing an empty gauge map for a thousand.
func sizeGroups(names map[string]*nameState, storeLen int) int {
	//: the smaller bound of the two.
	return min(len(names), storeLen)
}

// sortPoints orders one name's series by attribute set so a snapshot renders
// identically twice in a row given the same values.
func sortPoints[V any](points []V, attrsOf func(V) []coremetrics.AttrValue) {
	//: a single-series name — the overwhelming majority — is already ordered,
	//: and the call would otherwise cost a func value per name.
	if len(points) < minSortableSeries {
		//: nothing to order.
		return
	}
	//: lexicographic on the sorted attribute sets.
	slices.SortFunc(points, func(a, b V) int {
		//: compare the identities, never the values.
		return compareAttrs(attrsOf(a), attrsOf(b))
	})
}

// sumAttrs projects a sum series' attribute set.
func sumAttrs(p coremetrics.SumValue) []coremetrics.AttrValue {
	//: identity only.
	return p.Attrs
}

// gaugeAttrs projects a gauge series' attribute set.
func gaugeAttrs(p coremetrics.GaugeValue) []coremetrics.AttrValue {
	//: identity only.
	return p.Attrs
}

// histogramAttrs projects a histogram series' attribute set.
func histogramAttrs(p coremetrics.HistogramValue) []coremetrics.AttrValue {
	//: identity only.
	return p.Attrs
}
