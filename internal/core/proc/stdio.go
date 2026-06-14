// Package proc — the StdioMode value: how a spawned child's standard streams
// (stdin, stdout, stderr) are connected.
package proc

import "strconv"

// StdioMode selects how a spawned child's standard streams are wired. The zero
// value is StdioInherit, preserving the historical behaviour where the child
// shares the parent's stdin/stdout/stderr (systemd StandardOutput=/StandardInput=
// is the analogue, but the choice is generic to anything that spawns processes).
type StdioMode uint8

const (
	// StdioInherit shares the parent's stdin/stdout/stderr with the child — the
	// default, and the only behaviour that existed before per-process wiring.
	StdioInherit StdioMode = iota
	// StdioNull connects every stream to the platform null device: the child's
	// output is discarded and its stdin returns EOF immediately.
	StdioNull
	// StdioCapture connects the child to the Spec's Stdout/Stderr writers and
	// Stdin reader; a nil writer or reader for a given stream falls back to the
	// null device for that one stream.
	StdioCapture
)

// stdioModeNames maps each defined mode to its canonical lowercase name.
var stdioModeNames = map[StdioMode]string{
	StdioInherit: "inherit",
	StdioNull:    "null",
	StdioCapture: "capture",
}

// String returns the lowercase mode name ("inherit", "null", "capture"), or
// "stdiomode(N)" for an out-of-range value.
func (m StdioMode) String() string {
	//: a known mode resolves to its canonical lowercase name.
	if name, ok := stdioModeNames[m]; ok {
		//: table hit — return the canonical name verbatim.
		return name
	}
	//: an out-of-range value stays legible with its numeric form.
	return "stdiomode(" + strconv.Itoa(int(m)) + ")"
}

// Known reports whether m is one of the defined StdioInherit/StdioNull/StdioCapture
// modes (i.e. m <= StdioCapture).
func (m StdioMode) Known() bool {
	//: the defined modes are the contiguous range [StdioInherit, StdioCapture].
	return m <= StdioCapture
}
