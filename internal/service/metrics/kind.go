// Package metrics — instrument-kind guard for name reuse.
package metrics

import coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

// instrumentKind discriminates a name's bound instrument type.
type instrumentKind int

const (
	// kindCounter marks a name bound to a Counter.
	kindCounter instrumentKind = iota
	// kindGauge marks a name bound to a Gauge.
	kindGauge
	// kindHistogram marks a name bound to a Histogram.
	kindHistogram
)

// assertKind panics with InstrumentKindConflict if name is already bound to an
// instrument of a kind other than want. Caller holds mu.
func (m *memMeter) assertKind(name string, want instrumentKind) {
	//: a name present in another kind's map is a programmer error.
	_, isCounter := m.counters[name]
	_, isGauge := m.gauges[name]
	_, isHistogram := m.histograms[name]
	//: detect a binding under a different kind.
	clash := (isCounter && want != kindCounter) ||
		(isGauge && want != kindGauge) ||
		(isHistogram && want != kindHistogram)
	//: surface the typed conflict loudly at the call site.
	if clash {
		//: panic so the mis-typed instrument fetch is caught in development.
		panic(coremetrics.InstrumentKindConflict.Error())
	}
}
