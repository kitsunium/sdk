// Package metrics — the sibling port that registers asynchronous instruments.
package metrics

// AsyncMeter registers ASYNCHRONOUS instruments: a callback read once per
// Collect instead of a handle written to per observation. It is the right shape
// whenever the value already exists somewhere and only needs reading — a
// runtime counter, a queue length, a cache size — because instrumenting those
// synchronously means finding every mutation site.
//
// Registrations accumulate: registering a second callback under one name adds
// it rather than replacing it, which is what the OTel API specifies and what
// lets two packages contribute to the same instrument. There is no
// unregistration — an observable is declared at wiring time and lives as long
// as the meter.
//
// A sibling interface rather than three more methods on Meter, per ADR 0039.
type AsyncMeter interface {
	// ObservableCounter registers a monotonic sum read at collection time.
	// The callback reports the ABSOLUTE total.
	ObservableCounter(name string, observe Int64Callback)
	// ObservableUpDownCounter registers a non-monotonic sum read at
	// collection time. The callback reports the ABSOLUTE total.
	ObservableUpDownCounter(name string, observe Int64Callback)
	// ObservableGauge registers a sampled reading taken at collection time.
	ObservableGauge(name string, observe Float64Callback)
}
