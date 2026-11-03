package metrics

import (
	"strings"
	"sync"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
)

// ObserverConfig tunes RegisterObservers. The zero value registers the names
// as the observers spell them.
type ObserverConfig struct {
	// Underscore spells every name with '_' instead of '.'
	// (kitsunium_cache_hits), for a meter a Prometheus scrape reads.
	Underscore bool
}

// statPoint is one reported value of a statistic and its attributes.
type statPoint struct {
	// value is the reported absolute value.
	value int64
	// attrs are the attributes the observer reported it with.
	attrs []coreotel.AttrValue
}

// statSeries is one statistic of an observer: its kind and the points the
// collection in progress reported.
type statSeries struct {
	// kind decides the instrument.
	kind coremetrics.StatKind
	// points is the current collection's reports, reused between collections.
	points []statPoint
}

// statReader reads one observer once per collection for all its
// instruments: the first callback of a collection calls Observe, the others
// read what it stored — the meter runs each callback once per Collect,
// serialised, so counting the callbacks served marks where a collection
// begins.
type statReader struct {
	// mu guards everything below against a meter collected from two goroutines.
	mu sync.Mutex
	// observer is the engine read.
	observer coremetrics.Observer
	// series maps a reported name to its statistic.
	series map[string]*statSeries
	// served counts the callbacks of the current collection; callbacks is
	// how many one collection runs.
	served, callbacks int
	// record is r.store, bound once so Observe receives no fresh closure.
	record coremetrics.ReportStat
}

// RegisterObservers registers every statistic each observer reports as an
// observable instrument on meter (#871): Observe is called once here to read
// the names, kinds and units — a StatCounter becomes an observable counter, a
// StatGauge an observable up-down counter, the unit bound where meter is a
// UnitDescriber —, then ONCE per collection, its values feeding all of that
// observer's instruments, so an engine's Stats is read once per scrape
// whatever it reports. cfg.Underscore spells the names with '_' for a meter a
// Prometheus scrape reads. A statistic reported with no kind is dropped;
// registering one observer twice panics as any duplicate registration does.
func RegisterObservers(meter coremetrics.AsyncMeter, cfg ObserverConfig, observers ...coremetrics.Observer) {
	units, _ := meter.(coremetrics.UnitDescriber)
	//: each observer its own reader and instruments.
	for _, observer := range observers {
		registerObserver(meter, units, cfg, observer)
	}
}

// registerObserver describes observer once and registers one instrument per
// statistic, all fed by one reader.
func registerObserver(meter coremetrics.AsyncMeter, units coremetrics.UnitDescriber, cfg ObserverConfig, observer coremetrics.Observer) {
	reader := &statReader{observer: observer, series: map[string]*statSeries{}}
	reader.record = reader.store
	described := describeStats(observer)
	reader.callbacks = len(described)
	//: one instrument per statistic, named as the observer spells it.
	for _, stat := range described {
		name := spellStat(stat.name, cfg.Underscore)
		reader.series[stat.name] = &statSeries{kind: stat.kind}
		registerStat(meter, name, stat, reader)
		//: the unit, where the meter keeps one.
		if units != nil && stat.unit != "" {
			units.Unit(name, stat.unit)
		}
	}
}

// describedStat is a statistic as the registration's Observe reported it.
type describedStat struct {
	name string
	kind coremetrics.StatKind
	unit string
}

// describeStats calls Observe once and returns each named statistic with a
// kind, in report order, once.
func describeStats(observer coremetrics.Observer) []describedStat {
	var described []describedStat
	seen := map[string]bool{}
	observer.Observe(func(name string, kind coremetrics.StatKind, unit string, _ int64, _ ...coreotel.AttrValue) {
		//: a statistic with no kind, or one already described, adds nothing.
		if seen[name] || (kind != coremetrics.StatCounter && kind != coremetrics.StatGauge) {
			return
		}
		seen[name] = true
		described = append(described, describedStat{name: name, kind: kind, unit: unit})
	})
	//: the statistics, once each.
	return described
}

// spellStat is name with '.' turned into '_' when underscore is set.
func spellStat(name string, underscore bool) string {
	//: Prometheus's grammar has no '.'.
	if underscore {
		return strings.ReplaceAll(name, ".", "_")
	}
	//: the observer's spelling.
	return name
}

// registerStat registers the instrument of stat under name.
func registerStat(meter coremetrics.AsyncMeter, name string, stat describedStat, reader *statReader) {
	callback := func(observe coremetrics.ObserveInt64) {
		reader.emit(stat.name, observe)
	}
	//: a count that only grows, or a level.
	if stat.kind == coremetrics.StatCounter {
		meter.ObservableCounter(name, callback)
		return
	}
	meter.ObservableUpDownCounter(name, callback)
}

// emit reports name's points of the collection in progress, reading the
// observer on the collection's first callback.
func (r *statReader) emit(name string, observe coremetrics.ObserveInt64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	//: the first callback of this collection reads the engine for all of them.
	if r.served == 0 {
		//: the previous collection's points are forgotten first.
		for _, series := range r.series {
			series.points = series.points[:0]
		}
		r.observer.Observe(r.record)
	}
	r.served = (r.served + 1) % r.callbacks
	series := r.series[name]
	//: each point the engine reported for this statistic.
	for _, point := range series.points {
		observe(point.value, point.attrs...)
	}
}

// store keeps one reported point for its statistic; a name or a kind the
// registration did not describe is dropped.
func (r *statReader) store(name string, kind coremetrics.StatKind, _ string, value int64, attrs ...coreotel.AttrValue) {
	series, ok := r.series[name]
	//: only the statistics registered, with the kind they were registered as.
	if !ok || series.kind != kind {
		return
	}
	series.points = append(series.points, statPoint{value: value, attrs: attrs})
}
