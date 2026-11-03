package metrics

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// The two kinds of engine statistic an Observer reports (#871).
const (
	// StatCounter is a count that only grows while the engine lives — hits,
	// batches sent —, carried as an observable counter.
	StatCounter StatKind = 1
	// StatGauge is a level read now — entries held, a backlog —, carried as an
	// observable up-down counter.
	StatGauge StatKind = 2
)

// StatKind says how one of an engine's statistics reaches a meter: a
// StatCounter as an observable counter, a StatGauge as an observable up-down
// counter. The zero value is no kind, and a statistic reported with it is
// dropped.
type StatKind uint8

// ReportStat reports one of an engine's statistics from inside an Observer's
// Observe: its name, its kind, its UCUM unit, its current ABSOLUTE value and
// the attributes that tell its series apart. Valid only for the duration of
// the Observe call it was handed to.
type ReportStat func(name string, kind StatKind, unit string, value int64, attrs ...coreotel.AttrValue)
