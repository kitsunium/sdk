package metrics

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// HistogramValue is one series of a histogram metric: the attribute set that
// identifies it, the explicit bucket upper bounds, the count per bucket
// (len == len(Bounds)+1, the last being the +Inf overflow), and the Sum/Count
// of every observation in the window.
type HistogramValue struct {
	// Attrs is the series' attribute set, sorted by Key; nil for the
	// dimensionless series. See SumValue.Attrs for the aliasing rule.
	Attrs []coreotel.AttrValue
	// Bounds holds the histogram's explicit upper bounds, ascending. OTLP
	// calls this field explicit_bounds.
	Bounds []float64
	// Counts holds one count per bucket plus the +Inf overflow slot. The
	// counts are PER BUCKET, not cumulative — a format that wants a
	// cumulative ladder (Prometheus) builds it as it walks.
	Counts []uint64
	// Sum is the total of every observed value in the window; 0 and
	// meaningless when NoSum is true.
	Sum float64
	// Count is how many observations the window holds.
	Count uint64
	// NoSum is true when the source keeps no total of what it counted — an
	// observable histogram read off runtime/metrics, whose histograms carry
	// counts and no sum. Sum is then not a reading, and every exporter says
	// so rather than print it: OTLP/JSON writes the optional sum as absent,
	// the Prometheus connector writes no _sum line, the text exporter is
	// unchanged (it prints the count). The zero value is a histogram that
	// knows its sum, which every synchronous histogram does.
	NoSum bool
	// Exemplars holds at most one exemplar per bucket, in bucket order: the
	// last measurement of the window recorded with RecordContext under a
	// SAMPLED span (ADR 0051). Nil when none was.
	Exemplars []ExemplarValue
}
