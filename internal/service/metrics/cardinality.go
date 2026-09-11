// Package metrics — the cardinality bound.
package metrics

// DefaultMaxSeriesPerInstrument is the bound a Meter uses when MeterConfig
// leaves the knob unset. 2000 distinct label sets per instrument name is the
// figure the OpenTelemetry SDKs settled on for the same problem: high enough
// that a sanely-labelled metric never reaches it, low enough that a metric
// which does reach it has not yet cost meaningful memory.
const DefaultMaxSeriesPerInstrument int = 2000

// MeterConfig configures a Meter's cardinality bound.
type MeterConfig struct {
	// MaxSeriesPerInstrument bounds how many distinct label sets ONE
	// instrument name may hold. Beyond it, further label sets fold into a
	// single aggregated overflow series rather than allocating — see
	// seriesStore.admit for that policy and what it costs.
	//
	// Non-positive CLAMPS to DefaultMaxSeriesPerInstrument. It does NOT mean
	// "unbounded", and there is no setting that does (ADR 0031): a caller who
	// leaves this zero has not decided that memory is free, they have not yet
	// learned the question exists, and the zero value must not be the
	// dangerous answer. A caller who genuinely wants a huge bound types a
	// huge number, and the number is then in their source where a reviewer
	// can see it.
	//
	// The bound is PER NAME rather than per meter so that one exploding label
	// on http_requests_total cannot starve db_queries_total of the series it
	// needs; each metric's failure stays its own.
	MaxSeriesPerInstrument int
}
