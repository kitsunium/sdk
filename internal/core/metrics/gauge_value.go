// Package metrics — the exportable gauge point: a sampled reading, which covers
// no window and therefore carries no temporality.
package metrics

// GaugeValue is one series of a gauge metric: the attribute set that identifies
// it plus its instantaneous reading.
type GaugeValue struct {
	// Attrs is the series' attribute set, sorted by Key; nil for the
	// dimensionless series. See SumValue.Attrs for the aliasing rule.
	Attrs []AttrValue
	// Value is the last reading taken.
	Value float64
}
