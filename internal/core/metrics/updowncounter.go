// Package metrics — the UpDownCounter instrument.
package metrics

// UpDownCounter is a NON-MONOTONIC sum: a running total that may go down as
// well as up — in-flight requests, queue depth, open connections.
//
// It is not a Gauge. A gauge is a sampled reading with no arithmetic behind it
// ("the temperature is 21.5"); an up-down counter is a total every call site
// contributes to ("this handler took one slot, that one released it"), which is
// why it is additive and a gauge is not. In the OTel data model both a Counter
// and an UpDownCounter produce a Sum, and MONOTONICITY IS A FIELD OF THAT SUM
// rather than a second point type — which is exactly why the two interfaces
// exist at the API and only one shape exists in SumMetricValue.
//
// It is a sibling of Counter rather than a widening of it, and structurally
// distinct from it by Dec: a Counter is not silently usable where an
// UpDownCounter is required.
type UpDownCounter interface {
	// Add adjusts the total by delta, which may be negative.
	Add(delta int64)
	// Inc adds one.
	Inc()
	// Dec subtracts one.
	Dec()
}
