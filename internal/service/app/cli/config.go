package cli

import (
	"io"
	"os"
)

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
