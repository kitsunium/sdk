// Package logger: logger.go declares the Logger interface — the primary entry
// point consumers interact with. A Logger binds a Handler and exposes
// ergonomic Log / With / Enabled operations over it.
package logger

import (
	"context"

	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// Logger is the primary logging interface. Implementations MUST be safe for
// concurrent use by multiple goroutines.
type Logger interface {
	// Log emits a RecordEvent at the given level with the supplied message
	// and attrs. Implementations SHOULD short-circuit when Enabled returns
	// false to avoid unnecessary formatting work.
	Log(ctx context.Context, lv level.Level, msg string, attrs ...AttrValue)
	// With returns a derived Logger whose emitted records always include the
	// supplied attrs. The receiver MUST NOT be mutated.
	With(attrs ...AttrValue) (child Logger)
	// Enabled reports whether a record at the given level would be emitted.
	Enabled(ctx context.Context, lv level.Level) (enabled bool)
}
