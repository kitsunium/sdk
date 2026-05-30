// Package tee — declares the Config value type consumed by New. Kept in its
// own file per the one-exported-struct rule.
package tee

import corelogger "github.com/kitsunium/sdk/internal/core/logger"

// Config configures a TeeSink: the primary sinks every record is fanned out
// to, plus an optional spill sink that receives a record only when every
// primary rejected it.
type Config struct {
	// Primaries receive every record; their errors are aggregated.
	Primaries []corelogger.Sink
	// Spill is the optional dead-letter sink. It receives a record only when
	// all primaries failed. A nil Spill disables the dead-letter seam.
	Spill corelogger.Sink
}
