// Package writer — ConsoleConfig value type for the "console" writer.
package writer

import "github.com/kitsunium/sdk/internal/core/logger/level"

// ConsoleStream selects which standard stream the console writer targets.
type ConsoleStream uint8

const (
	// ConsoleStderr writes to os.Stderr. It is the zero value, so a
	// ConsoleConfig{} targets stderr by default: a caller who has not named a
	// stream has not made a choice, and stdout may be the process's protocol
	// channel (ADR 0030). The order of this block is load-bearing — the zero
	// value is the contract, not the name that happens to come first.
	ConsoleStderr ConsoleStream = iota
	// ConsoleStdout writes to os.Stdout. Reachable only by naming it.
	ConsoleStdout
)

// ConsoleConfig configures the "console" writer. The zero value is valid and
// targets os.Stderr at the handler-global level.
type ConsoleConfig struct {
	// Stream selects stderr (default) or stdout.
	Stream ConsoleStream
	// MinLevel is the optional per-writer severity floor; the zero value
	// inherits the handler-global level.
	MinLevel level.Level
}
