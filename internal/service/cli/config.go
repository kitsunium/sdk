// Package cli — the engine's configuration: two writers, and deliberately
// nothing else.
package cli

import (
	"io"
	"os"
)

// Config configures one [New]. Its zero value is a working configuration.
//
// It carries two writers and nothing else. There is no Signals field, no
// shutdown budget, no logger, no config source and no env prefix: every one of
// those is a domain the SDK already ships, and a CLI that grew its own copy
// would be a second, weaker one that the first would eventually disagree with.
// ADR 0065 §D6 lists what this domain composes and what it refuses to redo.
//
// Config is a published concrete shape (pkg/v1/cli.Config aliases it), so ADR
// 0040 applies: it may still change while the module is v0, said out loud, and
// not after v1.
type Config struct {
	// Output is where a command's OWN output goes. It reaches an [Action] as
	// InvocationValue.Output, so a command never has to reach for a process
	// stream itself.
	//
	// The zero value resolves to os.Stderr, NOT os.Stdout. ADR 0030 makes
	// stdout a protocol channel that no SDK default may claim: a tool whose
	// output is a JSON document on a pipe sets Output: os.Stdout in main,
	// which is one visible line at the one place that can honestly make the
	// choice. A zero value is what somebody who has not yet learned the
	// question exists chose, so it must not be the dangerous one.
	Output io.Writer
	// ErrOutput is where the generated help, and the usage that follows a bad
	// command line, are written. The zero value resolves to os.Stderr.
	//
	// It is a SECOND writer rather than the same one so that setting Output to
	// os.Stdout for a machine-readable tool does not put a help page in the
	// middle of the document a consumer is parsing.
	//
	// What is deliberately NOT written here is the error itself. The engine
	// owns the help — it is generated from the declarations and only the SDK
	// can render it — and the caller owns how an error is presented, because a
	// caller may want it as a line on stderr, as a log record, or as JSON. Two
	// voices printing one failure is how a CLI ends up saying it twice.
	ErrOutput io.Writer
}

// resolveOutput returns the writer a command prints to.
func (c Config) resolveOutput() io.Writer {
	//: an unset writer is the ADR 0030 default, never os.Stdout.
	if c.Output == nil {
		//: stderr is the safe channel: it is not a protocol.
		return os.Stderr
	}
	//: the caller made the choice explicitly.
	return c.Output
}

// resolveErrOutput returns the writer help and usage are written to.
func (c Config) resolveErrOutput() io.Writer {
	//: an unset writer is the ADR 0030 default, never os.Stdout.
	if c.ErrOutput == nil {
		//: help is a diagnostic; stderr is where diagnostics belong.
		return os.Stderr
	}
	//: the caller made the choice explicitly.
	return c.ErrOutput
}
