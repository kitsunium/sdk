// Package metrics — the Meter (instrument factory + collector).
package metrics

// Meter mints named, labelled instruments and collects their values.
//
// A SERIES is one instrument name plus one label set, and it is the unit of
// identity: repeated calls with the same name and the same labels return the
// SAME instrument, so observations from every call site accumulate in one
// place. Label ORDER is not part of the identity — the label set is a set, and
// {a,b} and {b,a} resolve to the same series. Passing no labels at all is
// legitimate and names the dimensionless series, which is what every
// pre-label call site already asked for.
//
// A name reused across instrument KINDS is a programmer error. So is a label
// set with an empty or repeated key: both are structure, fixed at the call
// site, and an implementation is expected to refuse them loudly.
//
// Implementations MUST be safe for concurrent use, and MUST bound how many
// distinct label sets one name may hold — an unbounded label set is a memory
// incident, not a reporting inconvenience.
type Meter interface {
	Counter(name string, labels ...LabelValue) Counter
	Gauge(name string, labels ...LabelValue) Gauge
	Histogram(name string, buckets []float64, labels ...LabelValue) Histogram
	// Collect returns a point-in-time SnapshotValue of every series.
	Collect() SnapshotValue
}
