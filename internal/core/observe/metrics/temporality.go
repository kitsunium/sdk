package metrics

// The three wire spellings, which the text exporter emits verbatim.
const (
	temporalityUnspecifiedText string = "unspecified"
	temporalityDeltaText       string = "delta"
	temporalityCumulativeText  string = "cumulative"
)

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

// resolved is Temporality.Resolved's body: decl_gen.go writes Temporality.Resolved, from the
// design, as one call of it.
func (t Temporality) resolved() Temporality {
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
