package proc_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// String must stay legible for a value outside the defined range: a mode read
// from a config file or a future release lands here, and "stdiomode(9)" tells
// an operator what happened where an empty string would not.
func Test_StdioMode_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mode coreproc.StdioMode
		want string
	}
	tests := []tc{
		{"the zero value is inherit", coreproc.StdioInherit, "inherit"},
		{"null", coreproc.StdioNull, "null"},
		{"capture", coreproc.StdioCapture, "capture"},
		{"one past the range", coreproc.StdioCapture + 1, "stdiomode(3)"},
		{"far out of range", coreproc.StdioMode(200), "stdiomode(200)"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.mode.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Known is the guard a caller uses before trusting a decoded value; it must
// answer for the whole contiguous range and nothing beyond it.
func Test_StdioMode_Known(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mode coreproc.StdioMode
		want bool
	}
	tests := []tc{
		{"inherit", coreproc.StdioInherit, true},
		{"null", coreproc.StdioNull, true},
		{"capture", coreproc.StdioCapture, true},
		{"the first value past the range", coreproc.StdioCapture + 1, false},
		{"the largest representable value", coreproc.StdioMode(255), false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.mode.Known(); got != c.want {
			t.Errorf("Known() = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The zero value must be StdioInherit: a Spec that never mentions stdio keeps
// the behaviour that existed before per-process wiring, which is what makes
// the field additive rather than breaking.
func Test_StdioMode_zeroValue(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  coreproc.StdioMode
		want coreproc.StdioMode
	}
	tests := []tc{
		{"the declared zero value", coreproc.StdioMode(0), coreproc.StdioInherit},
		{"a field left unset", coreproc.Spec{}.Stdio, coreproc.StdioInherit},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if c.got != c.want {
			t.Errorf("mode = %v, want %v", c.got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
