// Package logger — exposes the runtime-tunable level surface: ParseLevel (the
// strict inverse of a lowercased Level.String), the Leveler one-method port, and
// LevelVar, an atomically mutable threshold holder a custom sink or gate consults
// on each record to retune a live logger's floor without rebuilding the pipeline.
package logger

import "github.com/kitsunium/sdk/internal/core/logger/level"

// Leveler is the stable alias for the internal one-method level port. A custom
// Sink reads Level on each record so the threshold can change at runtime; both
// LevelVar and any constant-returning type satisfy it.
type Leveler = level.Leveler

// LevelVar is the stable alias for the atomic Level holder. Its zero value
// reports LevelInfo; Set and Level are safe for concurrent use, so one
// goroutine can retune the floor while a sink reads it on the hot path.
type LevelVar = level.Var

// NewLevelVar returns a LevelVar seeded with initial. Hold the returned pointer
// where a sink or gate can read its Level, then call Set to raise or lower the
// live threshold. Pair it with ParseLevel to retune from an env or flag value.
func NewLevelVar(initial Level) *LevelVar {
	//: delegate to the core constructor; this façade adds no behaviour.
	return level.NewVar(initial)
}

// ParseLevel maps a canonical lowercase level name (debug / info / warn /
// error) to its Level, the strict inverse of a lowercased Level.String. Input
// is trimmed and lowercased before matching. Unknown names return the zero
// Level and a redacted LevelUnknown error — the offending input is never echoed.
//
// Unlike the internal best-effort config path, this surface reports the miss so
// a caller validating an env/flag value can reject it rather than silently
// falling back to Info.
func ParseLevel(name string) (lvl Level, err error) {
	//: delegate to the core parser; the typed sentinel propagates unchanged.
	return level.ParseLevel(name)
}
