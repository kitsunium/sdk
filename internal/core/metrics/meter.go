// Package metrics — the Meter: the frozen instrument factory + collector.
package metrics

// Meter mints named, attributed instruments and collects their values.
//
// A SERIES is one instrument name plus one attribute set, and it is the unit of
// identity: repeated calls with the same name and the same attributes return
// the SAME instrument, so observations from every call site accumulate in one
// place. Attribute ORDER is not part of the identity — the set is a set, and
// {a,b} and {b,a} resolve to the same series. Passing no attributes at all is
// legitimate and names the dimensionless series.
//
// A name reused across instrument KINDS is a programmer error. So is an
// attribute set with an empty key, a repeated key, or a value no constructor
// ever set: all three are structure, fixed at the call site, and an
// implementation is expected to refuse them loudly.
//
// Implementations MUST be safe for concurrent use, and MUST bound how many
// distinct attribute sets one name may hold — an unbounded attribute set is a
// memory incident, not a reporting inconvenience.
//
// This interface is FROZEN. `pkg/v1/metrics.Meter` is a type alias to it, Go
// interfaces are structural, and adding a method would break every downstream
// implementer at compile time with no deprecation window (ADR 0039). The
// instruments it lacks live on UpDownMeter and AsyncMeter, and FullMeter is the
// union every in-tree constructor actually returns.
type Meter interface {
	Counter(name string, attrs ...AttrValue) Counter
	Gauge(name string, attrs ...AttrValue) Gauge
	Histogram(name string, buckets []float64, attrs ...AttrValue) Histogram
	// Collect returns a point-in-time SnapshotValue of every series.
	//
	// Under TemporalityDelta it is a MUTATION: it consumes the window it
	// reports. Two independent collectors would then each receive half the
	// observations, so a delta meter has exactly one reader.
	Collect() SnapshotValue
}
