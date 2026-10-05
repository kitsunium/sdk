package proc

import "strconv"

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

// known is StdioMode.Known's body: decl_gen.go writes StdioMode.Known, from the
// design, as one call of it.
func (m StdioMode) known() bool {
	//: the defined modes are the contiguous range [StdioInherit, StdioCapture].
	return m <= StdioCapture
}
