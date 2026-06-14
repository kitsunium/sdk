//go:build !unix

// Package proc — non-Unix signal name table (Windows): the portable subset.
package proc

import "syscall"

// signalNames maps the portable signal subset to canonical names on non-Unix
// platforms (Windows). Only the signals the standard library reliably honours
// there are listed; everything else is unsupported and Parse rejects it with
// UnknownSignal. Declared as a literal (no init) per KTN-FUNC-NOINIT.
var signalNames = map[Signal]string{
	Signal(syscall.SIGINT):  "SIGINT",
	Signal(syscall.SIGTERM): "SIGTERM",
	Signal(syscall.SIGKILL): "SIGKILL",
	Signal(syscall.SIGHUP):  "SIGHUP",
	Signal(syscall.SIGQUIT): "SIGQUIT",
}
