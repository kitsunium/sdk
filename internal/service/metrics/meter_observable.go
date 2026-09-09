// Package metrics — the asynchronous instruments: registration, and the read
// that happens once per Collect.
package metrics

import coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

// observer is one registered asynchronous instrument. Exactly one of the two
// callbacks is set, decided by kind: the two sum kinds report integers, the
// gauge kind reports a double.
type observer struct {
	name    string
	kind    instrumentKind
	int64Cb coremetrics.Int64Callback
	floatCb coremetrics.Float64Callback
}

// ObservableCounter registers a monotonic sum read at collection time. The
// callback reports the ABSOLUTE total; under delta temporality the meter
// differences successive reports itself.
//
// Registrations accumulate rather than replace, which is what the OTel API
// specifies: two packages may both contribute to one instrument name. Binding
// the name here — at registration, not at the first collection — is what makes
// a conflict with a synchronous instrument of the same name fail at wiring time
// rather than inside a scrape.
func (m *memMeter) ObservableCounter(name string, observe coremetrics.Int64Callback) {
	//: bind and record; a nil callback is refused before it can be run.
	m.register(observer{name: name, kind: kindObservableCounter, int64Cb: observe})
}

// ObservableUpDownCounter registers a non-monotonic sum read at collection time.
func (m *memMeter) ObservableUpDownCounter(name string, observe coremetrics.Int64Callback) {
	//: same registration path, different monotonicity on the metric.
	m.register(observer{name: name, kind: kindObservableUpDownCounter, int64Cb: observe})
}

// ObservableGauge registers a sampled reading taken at collection time.
func (m *memMeter) ObservableGauge(name string, observe coremetrics.Float64Callback) {
	//: gauges report a double and carry no temporality.
	m.register(observer{name: name, kind: kindObservableGauge, int64Cb: nil, floatCb: observe})
}

// register binds the name to its kind and appends the observer.
//
// A nil callback is dropped rather than stored: it is a registration that
// observes nothing, and calling it at collection time would panic inside a
// scrape — far from the wiring that caused it. The name is still bound, so the
// kind conflict a later synchronous fetch would cause is still detected.
func (m *memMeter) register(obs observer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: claim the name for this kind, or panic on a cross-kind reuse.
	m.bindName(obs.name, obs.kind)
	//: a callback that does not exist is not registered.
	if obs.int64Cb == nil && obs.floatCb == nil {
		//: name bound, nothing to run.
		return
	}
	//: accumulate — a second callback adds to the first.
	m.observers = append(m.observers, obs)
}

// runObservers reads every registered asynchronous instrument into its series.
// Caller holds collectMu and NO other lock.
//
// The observer list is copied out under the read lock and the callbacks run
// with no lock held, because a callback resolves series through the ordinary
// fetch path and would otherwise deadlock against the lock its own collection
// holds. A callback that calls Collect on this meter blocks on collectMu
// instead, which is a deadlock the documentation forbids rather than one the
// implementation can prevent.
func (m *memMeter) runObservers(delta bool) {
	m.mu.RLock()
	//: a slice-header copy; append under the write lock never rewrites the
	//: elements this view spans.
	regs := m.observers
	m.mu.RUnlock()
	//: nothing registered is the common case and costs one branch.
	if len(regs) == 0 {
		//: no asynchronous instruments.
		return
	}
	//: one callback at a time, in registration order.
	for _, obs := range regs {
		//: a gauge reports a double, the two sum kinds an integer.
		if obs.kind == kindObservableGauge {
			//: the closure lives for exactly this call.
			obs.floatCb(func(value float64, attrs ...coremetrics.AttrValue) {
				//: resolve or create, then publish the reading.
				m.observeGauge(obs.name, value, attrs)
			})
			//: next observer.
			continue
		}
		//: an integer-valued observable sum.
		obs.int64Cb(func(value int64, attrs ...coremetrics.AttrValue) {
			//: resolve or create, then convert to the meter's temporality.
			m.observeSum(obs.name, obs.kind, value, attrs, delta)
		})
	}
}

// observeSum publishes one absolute integer reading into its series, creating
// the series on first sight and honouring the cardinality bound exactly as a
// synchronous fetch does.
//
// A folded observable is worth stating plainly: past the bound every further
// attribute set lands in ONE overflow series, and each callback report
// OVERWRITES the previous one rather than adding to it, so the overflow series
// of an observable shows the last set reported and not their total. That is the
// honest consequence of an absolute-valued instrument meeting a fold, and it is
// why an observable's attributes should be a small fixed set.
func (m *memMeter) observeSum(
	name string, kind instrumentKind, value int64, attrs []coremetrics.AttrValue, delta bool,
) {
	//: same identity discipline as a synchronous fetch; this path runs once
	//: per series per collection, so the stack scratch is a convenience
	//: rather than a budget.
	var sortBuf [maxStackAttrs]coremetrics.AttrValue
	var keyBuf [seriesKeyCap]byte
	sorted := sortAttrs(sortBuf[:0], attrs)
	coremetrics.ValidateAttrs(sorted)
	key := appendSeriesKey(keyBuf[:0], kind, name, sorted)

	//: an existing series resolves under the read lock.
	sum, ok := m.sums.lookup(key)
	//: first sight creates it — as an OBSERVED sum, so a delta collection
	//: reads it instead of swapping it to zero.
	if !ok {
		//: monotonicity travels with the kind.
		sum = m.sums.admit(name, kind, key, sorted, observableBuilder(kind))
	}
	//: publish the absolute value, differenced when the reader is delta.
	sum.observe(value, delta)
}

// observeGauge publishes one sampled reading into its series.
func (m *memMeter) observeGauge(name string, value float64, attrs []coremetrics.AttrValue) {
	//: same identity discipline as observeSum.
	var sortBuf [maxStackAttrs]coremetrics.AttrValue
	var keyBuf [seriesKeyCap]byte
	sorted := sortAttrs(sortBuf[:0], attrs)
	coremetrics.ValidateAttrs(sorted)
	key := appendSeriesKey(keyBuf[:0], kindObservableGauge, name, sorted)

	//: an existing series resolves under the read lock.
	gauge, ok := m.gauges.lookup(key)
	//: first sight creates it.
	if !ok {
		//: an observable gauge is stored exactly like a synchronous one —
		//: a reading has no window to consume.
		gauge = m.gauges.admit(name, kindObservableGauge, key, sorted, newMemGauge)
	}
	//: a gauge reports the reading as taken.
	gauge.Set(value)
}

// observableBuilder returns the constructor for an observable sum of kind.
func observableBuilder(kind instrumentKind) func() *memSum {
	//: monotonicity is the only difference between the two.
	if kind == kindObservableCounter {
		//: monotonic and observed.
		return newObservableCounter
	}
	//: non-monotonic and observed.
	return newObservableUpDownCounter
}
