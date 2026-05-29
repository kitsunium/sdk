// Package writer — ConsoleConfig value type for the "console" writer.
package writer

import "github.com/kitsunium/sdk/internal/core/logger/level"

// ConsoleStream selects which standard stream the console writer targets.
type ConsoleStream uint8

const (
	// ConsoleStdout writes to os.Stdout. It is the zero value, so a
	// ConsoleConfig{} targets stdout by default.
	ConsoleStdout ConsoleStream = iota
	// ConsoleStderr writes to os.Stderr.
	ConsoleStderr
)

// ConsoleConfig configures the "console" writer. The zero value is valid and
// targets os.Stdout at the handler-global level.
type ConsoleConfig struct {
	// Stream selects stdout (default) or stderr.
	Stream ConsoleStream
	// MinLevel is the optional per-writer severity floor; the zero value
	// inherits the handler-global level.
	MinLevel level.Level
}
