// Package metrics — the Meter's configuration: cardinality bound, aggregation
// temporality, producing Resource, instrumentation Scope and clock.
package metrics

import (
	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// DefaultMaxSeriesPerInstrument is the bound a Meter uses when MeterConfig
// leaves the knob unset. 2000 distinct attribute sets per instrument name is
// the figure the OpenTelemetry SDKs settled on for the same problem: high
// enough that a sanely-attributed metric never reaches it, low enough that a
// metric which does reach it has not yet cost meaningful memory.
const DefaultMaxSeriesPerInstrument int = 2000

// MeterConfig configures a Meter. Every field has a resolved meaning when left
// unset — none of them is inert (ADR 0031).
type MeterConfig struct {
	// MaxSeriesPerInstrument bounds how many distinct attribute sets ONE
	// instrument name may hold. Beyond it, further sets fold into a single
	// aggregated overflow series rather than allocating — see
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
	// The bound is PER NAME rather than per meter so that one exploding
	// attribute on http_requests_total cannot starve db_queries_total of the
	// series it needs; each metric's failure stays its own.
	MaxSeriesPerInstrument int

	// Temporality says which window a collected sum or histogram covers.
	//
	// Unset CLAMPS to TemporalityCumulative, because that is what this meter
	// DOES when nothing tells it otherwise: it accumulates into atomics and
	// never resets them. TemporalityDelta makes Collect consume the window it
	// reports, which means a delta meter has exactly one reader — two
	// independent collectors would each receive part of the observations.
	// Any value that is neither of the three constants panics
	// (InvalidTemporality); it can only come from a cast.
	Temporality coremetrics.Temporality

	// Resource identifies the producer of the telemetry and is carried once
	// per snapshot. An absent service.name is filled with
	// coremetrics.UnknownService, which is what the OpenTelemetry
	// specification mandates rather than a value this SDK invented.
	Resource coremetrics.ResourceValue

	// Scope identifies the instrumentation that mints the instruments. An
	// empty Name resolves to coremetrics.DefaultScopeName; Version stays
	// empty when the caller has none, because it is optional in the
	// specification and inventing one would be a claim about code this
	// package cannot see.
	Scope coremetrics.ScopeValue

	// Clock supplies the snapshot window's endpoints. Nil resolves to
	// clock.System, matching every other port in this SDK that takes one.
	// Only Now and Since are used, so the frozen two-method clock.Clock is
	// enough and a two-method test double still satisfies it (ADR 0039).
	Clock clock.Clock
}

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
		Resource: c.Resource.Normalized(),
		//: named, so a backend can tell this SDK's metrics from the app's.
		Scope: c.Scope.Normalized(),
		Clock: clk,
	}
}
