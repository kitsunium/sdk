// Package metrics — the sibling port that mints the non-monotonic sum.
package metrics

// UpDownMeter mints the non-monotonic sum Meter cannot — the instrument for a
// total that goes down as well as up.
//
// A sibling interface rather than a fourth method on Meter, per ADR 0039: Meter
// is published through a `pkg/v1` type alias, Go interfaces are structural, and
// widening one breaks every downstream implementer at compile time.
type UpDownMeter interface {
	UpDownCounter(name string, attrs ...AttrValue) UpDownCounter
}
