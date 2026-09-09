// Package metrics — aggregation temporality, the OTel concept that says which
// window a reported number covers.
package metrics

// The three wire spellings, which the text exporter emits verbatim.
const (
	temporalityUnspecifiedText string = "unspecified"
	temporalityDeltaText       string = "delta"
	temporalityCumulativeText  string = "cumulative"
)

// Temporality says which time window a metric's reported value covers. It is
// the single most load-bearing concept in the OpenTelemetry metrics data model,
// because the same number means two different things under the two settings and
// nothing in the number itself says which.
type Temporality uint8

const (
	// TemporalityUnspecified is the zero value and means "the caller has not
	// chosen". It is legal ONLY in a MeterConfig, where it resolves to
	// TemporalityCumulative; it never reaches a SnapshotValue.
	TemporalityUnspecified Temporality = iota
	// TemporalityDelta means each point covers the window since the previous
	// collection: (T1,T2], then (T2,T3]. The reported number is consumed by
	// the collection that reads it and starts again from zero.
	TemporalityDelta
	// TemporalityCumulative means each point covers the window since the
	// series started: (T0,T1], then (T0,T2]. The reported number is a running
	// total and a reader differences successive collections itself.
	TemporalityCumulative
)

// String returns the spelling used in diagnostics and in the text exporter.
func (t Temporality) String() string {
	//: one spelling per value; an out-of-range cast has no spelling.
	switch t {
	//: the unset value, which only a MeterConfig may carry.
	case TemporalityUnspecified:
		//: named rather than blank, so a diagnostic says what happened.
		return temporalityUnspecifiedText
	//: window since the previous collection.
	case TemporalityDelta:
		//: the OTLP spelling, lower-cased.
		return temporalityDeltaText
	//: window since the series started.
	case TemporalityCumulative:
		//: the OTLP spelling, lower-cased.
		return temporalityCumulativeText
	//: an out-of-range cast.
	default:
		//: the same word Resolved refuses on, so the two agree.
		return temporalityUnspecifiedText
	}
}

// Resolved maps the UNSET zero value onto the temporality a Meter actually
// implements, and refuses anything that is neither of the three constants.
//
// TemporalityUnspecified CLAMPS to TemporalityCumulative rather than refusing,
// and the reason is not convention: an in-memory meter accumulates into atomics
// and never resets them unless it is asked to, so "cumulative" is a DESCRIPTION
// of what the unconfigured meter does, not a value invented on the caller's
// behalf. Any other default would be a claim about the implementation that the
// implementation does not honour (ADR 0031 §clamp).
//
// An out-of-range value can only come from a deliberate cast — Temporality(7)
// is not something a caller reaches by forgetting a field — so it is a
// programming error and panics with InvalidTemporality rather than being
// quietly folded into the default, which would hide the cast forever.
func (t Temporality) Resolved() Temporality {
	//: one branch per legal value.
	switch t {
	//: the unset knob takes the temporality the meter implements.
	case TemporalityUnspecified, TemporalityCumulative:
		//: what an accumulating meter actually does.
		return TemporalityCumulative
	//: an explicit delta reader consumes each window.
	case TemporalityDelta:
		//: honoured as written.
		return TemporalityDelta
	//: anything else was cast into existence on purpose.
	default:
		//: fail at the constructor, not at the first scrape.
		panic(InvalidTemporality.Error())
	}
}
