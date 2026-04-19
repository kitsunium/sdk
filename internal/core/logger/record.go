// Package logger: record.go defines the RecordEvent value carried between
// Logger and Handler. It is an immutable snapshot of a single log event at
// the core boundary.
package logger

import (
	"time"

	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// RecordEvent captures a single log event; handlers are responsible for writing
// or forwarding it. Instances are intended to be used read-only after
// construction by the Logger implementation.
type RecordEvent struct {
	// Time is the instant the event occurred; zero means "fill in at handle time".
	Time time.Time
	// Level is the severity of the event.
	Level level.Level
	// Message is the human-readable description of the event.
	Message string
	// Attrs carries the structured key/value pairs attached to this event.
	Attrs []AttrValue
}
