// Package logger — re-exports the chainable Builder API and the
// slice-overload LogAttrs entry point. Both are routed through the
// internal service implementation, which owns the recycler that holds
// the steady-state hot-path at one allocation per emit.
package logger

import (
	"context"

	svclogger "github.com/kitsunium/sdk/internal/service/logger"
)

// Builder is the stable alias for the internal chainable Builder. Returned
// by Build, every typed accessor (Str / Int / Bool / …) returns the receiver
// so callers compose the chain in a single expression. Send terminates the
// chain — callers MUST NOT use the builder after Send.
type Builder = svclogger.Builder

// Build returns a chainable Builder bound to lg at the supplied level.
// Builders are recycled through a sync.Pool, so the steady-state per-call
// cost is one heap allocation per emit — the handler clones the accumulated
// attrs on Send, and that clone escapes. See BENCH.md.
func Build(lg Logger, lv Level) Builder {
	//: delegate to the internal Build entry point that owns the recycler.
	return svclogger.Build(lg, lv)
}

// LogAttrs is the slice-overload of Logger.Log that avoids the variadic
// slice allocation imposed by Logger.Log(... Attr). Pre-built attribute
// slices flow through this entry point without per-call boxing.
func LogAttrs(ctx context.Context, lg Logger, lv Level, msg string, attrs []Attr) {
	//: delegate to the internal LogAttrs entry point.
	svclogger.LogAttrs(ctx, lg, lv, msg, attrs)
}
