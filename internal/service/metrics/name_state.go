// Package metrics — per-instrument-name bookkeeping: kind binding and the
// cardinality tally.
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

// nameState is what a Meter knows about one instrument NAME, as opposed to one
// series: the kind it is bound to, how many series it has admitted, and the key
// of its overflow series once one exists. Guarded by memMeter.mu.
//
// Kind and bound both belong to the name rather than the series because labels
// vary within one metric and neither of these does: every series under
// http_requests_total is a counter, and they share one quota.
type nameState struct {
	kind instrumentKind
	// series counts ADMITTED label sets, excluding the overflow series. The
	// overflow series is deliberately outside the bound: it is the one extra
	// slot that makes the bound observable instead of silent.
	series int
	// overflowKey is the encoded series key of this name's overflow series,
	// computed on first overflow and cached. Empty until then.
	overflowKey string
}

// seriesCap is how many series this name holds, overflow included. Collect
// uses it as an exact capacity: without it a name with many series regrows its
// slice by doubling, and the copying that costs scales with cardinality — the
// axis the whole feature exists to keep affordable.
func (s *nameState) seriesCap() int {
	//: the overflow series is the one slot outside the bound.
	if s.overflowKey == "" {
		//: no overflow yet.
		return s.series
	}
	//: admitted series plus the aggregated one.
	return s.series + 1
}

// overflowSeries returns the key and label set of name's overflow series,
// computing the key once.
func (s *nameState) overflowSeries(name string) (key string, labels []coremetrics.LabelValue) {
	//: compute once per name, on the create path only.
	if s.overflowKey == "" {
		//: same encoding as any other series — the overflow label is a label.
		s.overflowKey = string(appendSeriesKey(nil, name, overflowLabels))
	}
	//: the label set is shared; the caller clones it before storing.
	return s.overflowKey, overflowLabels
}

// bindName returns the per-name state, binding the name to want on first use
// and panicking with InstrumentKindConflict when it is already bound to a
// different kind. Caller holds mu for writing.
//
// Fetching an instrument is not a fallible operation in the port's shape —
// Counter(name) returns a Counter, full stop — so there is nowhere to put an
// error. And the mistake is always a programming one: the same metric name
// bound to two kinds means one of the two call sites is wrong, and every value
// it records lands in an instrument nothing will ever read.
func (m *memMeter) bindName(name string, want instrumentKind) *nameState {
	state, ok := m.names[name]
	//: first sighting of the name claims the kind.
	if !ok {
		//: bind and record.
		state = &nameState{kind: want}
		m.names[name] = state
		//: nothing to conflict with yet.
		return state
	}
	//: a name reused across kinds is a programmer error.
	if state.kind != want {
		//: panic so the mis-typed instrument fetch is caught in development.
		panic(coremetrics.InstrumentKindConflict.Error())
	}
	//: same kind — hand back the existing bookkeeping.
	return state
}
