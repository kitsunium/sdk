// Package metrics — the qualifier set of one text-exporter "# metric" line.
package metrics

// metricHeader is everything one "# metric" line says about an instrument
// name. Each field is empty when the kind does not have it: a gauge has neither
// a temporality nor a monotonicity, and any metric may have no description.
//
// A struct rather than six parameters, which is the OPPOSITE of the call
// seriesStore.admit makes — and the difference is which path each one is on.
// admit runs per OBSERVATION, where Go's field-insensitive escape analysis
// turns a grouped argument into two heap allocations (BENCH.md §Caveats).
// This runs once per instrument NAME per export, at scrape rate, where the
// grouping costs nothing and reads better than six positional strings.
type metricHeader struct {
	name         string
	kind         string
	temporality  string
	monotonicity string
	description  string
}
