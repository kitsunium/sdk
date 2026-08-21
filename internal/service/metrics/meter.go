// Package metrics — the in-memory Meter (a core/metrics.Meter).
package metrics

import (
	"sync"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// memMeter is the in-memory Meter: name-keyed instrument maps guarded by an
// RWMutex (writers serialise instrument creation; the read-only Collect takes a
// read lock). The instruments themselves are lock-free. Satisfies core Meter.
type memMeter struct {
	mu         sync.RWMutex
	counters   map[string]*memCounter
	gauges     map[string]*memGauge
	histograms map[string]*memHistogram
}

// NewMeter returns a fresh in-memory Meter (Counter/Gauge/Histogram + Collect).
func NewMeter() coremetrics.Meter {
	//: start with empty instrument maps.
	return &memMeter{
		counters:   make(map[string]*memCounter, 0),
		gauges:     make(map[string]*memGauge, 0),
		histograms: make(map[string]*memHistogram, 0),
	}
}

// Counter returns the named counter, creating it once. A name already bound to a
// different instrument kind panics with InstrumentKindConflict.
func (m *memMeter) Counter(name string) coremetrics.Counter {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: a name reused across kinds is a programmer error.
	m.assertKind(name, kindCounter)
	//: return the existing instrument if present.
	if cnt, ok := m.counters[name]; ok {
		//: idempotent fetch.
		return cnt
	}
	//: otherwise create + store it.
	cnt := &memCounter{}
	m.counters[name] = cnt
	//: hand back the fresh counter.
	return cnt
}

// Gauge returns the named gauge, creating it once.
func (m *memMeter) Gauge(name string) coremetrics.Gauge {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: guard against a cross-kind name clash.
	m.assertKind(name, kindGauge)
	//: idempotent fetch when already present.
	if gge, ok := m.gauges[name]; ok {
		//: existing gauge.
		return gge
	}
	//: create + store.
	gge := &memGauge{}
	m.gauges[name] = gge
	//: hand back the fresh gauge.
	return gge
}

// Histogram returns the named histogram, creating it once with buckets (ignored
// if the histogram already exists).
func (m *memMeter) Histogram(name string, buckets []float64) coremetrics.Histogram {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: guard against a cross-kind name clash.
	m.assertKind(name, kindHistogram)
	//: idempotent fetch when already present.
	if hst, ok := m.histograms[name]; ok {
		//: existing histogram (buckets fixed at first creation).
		return hst
	}
	//: create + store with the supplied buckets.
	hst := newHistogram(buckets)
	m.histograms[name] = hst
	//: hand back the fresh histogram.
	return hst
}

// Collect copies every instrument into a SnapshotValue. Read-only — holds RLock.
func (m *memMeter) Collect() coremetrics.SnapshotValue {
	m.mu.RLock()
	defer m.mu.RUnlock()
	//: build the three instrument maps.
	snap := coremetrics.SnapshotValue{
		Counters:   make(map[string]int64, len(m.counters)),
		Gauges:     make(map[string]float64, len(m.gauges)),
		Histograms: make(map[string]coremetrics.HistogramValue, len(m.histograms)),
	}
	//: copy counters.
	for name, cnt := range m.counters {
		//: snapshot the cumulative total.
		snap.Counters[name] = cnt.load()
	}
	//: copy gauges.
	for name, gge := range m.gauges {
		//: snapshot the instantaneous value.
		snap.Gauges[name] = gge.load()
	}
	//: copy histograms.
	for name, hst := range m.histograms {
		//: snapshot the bucketed distribution.
		snap.Histograms[name] = hst.snapshot()
	}
	//: hand back the whole-meter copy.
	return snap
}
