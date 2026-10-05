package metrics

import (
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// DefaultMaxSeriesPerInstrument is the bound a Meter uses when MeterConfig
// leaves the knob unset. 2000 distinct attribute sets per instrument name is
// the figure the OpenTelemetry SDKs settled on for the same problem: high
// enough that a sanely-attributed metric never reaches it, low enough that a
// metric which does reach it has not yet cost meaningful memory.
const DefaultMaxSeriesPerInstrument int = 2000

// resolve returns the configuration a Meter is actually built from: every knob
// clamped or refused, nothing left inert.
func (c MeterConfig) resolve() MeterConfig {
	//: clamp the bound before anything can observe it.
	maxSeries := c.MaxSeriesPerInstrument
	//: a zero or negative bound is an unset knob, not a request for infinity.
	if maxSeries <= 0 {
		//: the documented working default.
		maxSeries = DefaultMaxSeriesPerInstrument
	}
	//: a nil clock is an unset knob, not a request for a frozen meter.
	clk := c.Clock
	//: fall back to the real one.
	if clk == nil {
		//: the same default every other port in this SDK takes.
		clk = clock.System
	}
	//: hand back a config with no unresolved field left.
	return MeterConfig{
		MaxSeriesPerInstrument: maxSeries,
		//: unset means cumulative; an out-of-range cast panics here.
		Temporality: c.Temporality.Resolved(),
		//: sorted, validated, and carrying service.name.
		Resource: coremetrics.NormalizeResource(c.Resource),
		//: named, so a backend can tell this SDK's metrics from the app's.
		Scope: coremetrics.NormalizeScope(c.Scope),
		Clock: clk,
	}
}
