// Package logger — declares the Handler interface that concrete
// implementations in sdk/internal/service satisfy. Handlers format
// RecordEvent values and write them to the backing sink.
package logger

import "context"

// Handler formats and emits RecordEvent values produced by a Logger.
// Implementations MUST be safe for concurrent use by multiple goroutines.
type Handler interface {
	// Enabled reports whether the handler would emit a record at the given
	// level, allowing callers to skip expensive attribute construction.
	Enabled(ctx context.Context, r RecordEvent) (enabled bool)
	// Handle formats and writes a RecordEvent. Returning a non-nil error
	// signals a sink failure; the Logger may drop the record regardless.
	Handle(ctx context.Context, r RecordEvent) (err error)
	// WithAttrs returns a derived Handler that prepends the given attrs to
	// every subsequent RecordEvent. The receiver MUST NOT be mutated.
	WithAttrs(attrs []AttrValue) (child Handler)
	// WithGroup returns a derived Handler that namespaces every subsequent
	// attribute key under the given group name (rendered as "group.key"
	// for text handlers, or as a nested object for structured handlers).
	// An empty name MUST be treated as a no-op so callers can pass user
	// input without guarding it.
	WithGroup(name string) (child Handler)
}
