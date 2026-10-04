// Package writer — ConsoleConfig value type for the "console" writer.
//
// Package writer — declares the optional config Decoder extension that a
// Factory MAY implement to translate a raw, parsed config map into its typed
// Config. It mirrors codec's Appender optional-extension convention: consumers
// detect support with a runtime type assertion and fall back to a default
// mapping that passes the raw map straight through when the extension is absent.
//
// A Factory that parses credentials or other sensitive material out of the raw
// map MUST NOT echo any option value into an error it returns; callers that
// surface a Decoder failure name only the writer and the failure kind.
package writer

import "github.com/kitsunium/sdk/internal/core/observe/logger/level"

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
