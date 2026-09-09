// Package metrics — the union of the frozen port and its two siblings.
package metrics

// FullMeter is the whole OTel instrument set behind one name: the frozen Meter
// plus both siblings. It is what every constructor in this SDK returns, and
// widening a returned VALUE from Meter to FullMeter is safe for the same reason
// widening clock.System from Clock to Timed was — every existing assignment
// into a Meter still compiles (ADR 0039 §Decision 1).
type FullMeter interface {
	Meter
	UpDownMeter
	AsyncMeter
}
