package metrics

import "slices"

// MaxUnitLen is the longest unit UnitDescriber accepts, in bytes: a UCUM unit
// is a short label a backend reads, never a document (ADR 0208).
const MaxUnitLen int = 63

// defaultBuckets is the OpenTelemetry SDK's default explicit bucket ladder,
// copied out by DefaultBuckets and never handed out itself.
var defaultBuckets = []float64{0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000}

// DefaultBuckets returns a fresh copy of the explicit bucket boundaries a
// histogram created with no buckets uses — the OpenTelemetry SDK's default
// (0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000).
// A copy each call, so no caller can change another's layout. They suit
// milliseconds; a histogram recording seconds (RecordDuration) passes its
// own.
func DefaultBuckets() []float64 {
	//: a fresh slice: the layout of one histogram is never another's.
	return slices.Clone(defaultBuckets)
}
