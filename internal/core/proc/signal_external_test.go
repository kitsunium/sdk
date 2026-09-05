package proc_test

import (
	"syscall"
	"testing"

	"github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestParseRoundTrip asserts Parse(s.String()) == s for the signals present in
// EVERY platform table, so the test compiles and runs on non-Unix targets too
// (Windows's syscall lacks SIGUSR1/SIGWINCH/etc.). The full Unix table is
// round-tripped in signal_unix_external_test.go. Central contract of issue #62.
func Test_Parse(t *testing.T) {
	t.Parallel()

	//: only signals in both the unix and non-unix tables round-trip everywhere.
	cases := []proc.Signal{
		proc.Signal(syscall.SIGHUP),
		proc.Signal(syscall.SIGINT),
		proc.Signal(syscall.SIGTERM),
		proc.Signal(syscall.SIGKILL),
		proc.Signal(syscall.SIGQUIT),
	}

	runCase := func(t *testing.T, want proc.Signal) {
		t.Helper()
		//: the canonical String form must parse straight back to the same value.
		got, err := proc.Parse(want.String())
		//: a known signal never errors on round-trip.
		if err != nil {
			t.Fatalf("Parse(%q) returned error: %v", want.String(), err)
		}
		//: identity is the contract: name out, same signal back in.
		if got != want {
			t.Fatalf("Parse(%q) = %d, want %d", want.String(), got, want)
		}
	}

	for _, tc := range cases {
		//: subtest per signal keeps a failure pinpointed to one name.
		t.Run(tc.String(), func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestParseForms asserts the three accepted spellings (SIG-prefixed, bare,
// numeric) and case-insensitivity all resolve to the same signal.
func Test_Parse_forms(t *testing.T) {
	t.Parallel()

	want := proc.Signal(syscall.SIGTERM)

	type parseCase struct {
		name  string
		input string
	}
	//: every spelling systemd configs use in the wild must map to SIGTERM.
	cases := []parseCase{
		{name: "canonical", input: "SIGTERM"},
		{name: "bare", input: "TERM"},
		{name: "lowercase", input: "sigterm"},
		{name: "mixedcase", input: "SigTerm"},
		{name: "numeric", input: "15"},
		{name: "padded", input: "  SIGTERM  "},
	}

	runCase := func(t *testing.T, tc parseCase) {
		t.Helper()
		got, err := proc.Parse(tc.input)
		//: a recognised spelling never errors.
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", tc.input, err)
		}
		//: all spellings collapse to the one canonical signal.
		if got != want {
			t.Fatalf("Parse(%q) = %d, want %d", tc.input, got, want)
		}
	}

	for _, tc := range cases {
		//: subtest per spelling isolates which form regressed.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestParseUnknown asserts unrecognised names and out-of-range numbers surface
// the typed UnknownSignal sentinel rather than a guess.
func Test_Parse_unknown(t *testing.T) {
	t.Parallel()

	//: neither a bogus name nor an impossible number names a real signal.
	for _, input := range []string{"SIGBOGUS", "NOPE", "999999", ""} {
		//: subtest per input keeps the failing case obvious.
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			_, err := proc.Parse(input)
			//: the error must carry the central UNKNOWN_SIGNAL code.
			if !errs.HasCode(err, proc.CodeUnknownSignal) {
				t.Fatalf("Parse(%q) err = %v, want CodeUnknownSignal", input, err)
			}
		})
	}
}

// The bridge to os.Signal must preserve the platform number exactly: the
// value crosses into os/exec and the kernel, where a remapped number would
// deliver the wrong signal rather than fail.
func Test_Signal_OS(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		sig  syscall.Signal
	}
	tests := []tc{
		{"SIGTERM", syscall.SIGTERM},
		{"SIGINT", syscall.SIGINT},
		{"SIGKILL", syscall.SIGKILL},
		{"SIGHUP", syscall.SIGHUP},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := proc.Signal(c.sig).OS(); got != c.sig {
			t.Errorf("OS() = %v, want %v", got, c.sig)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Int mirrors the underlying platform number, which is what a caller writing
// an exit status or a wait4 result compares against.
func Test_Signal_Int(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		sig  syscall.Signal
	}
	tests := []tc{
		{"SIGTERM", syscall.SIGTERM},
		{"SIGINT", syscall.SIGINT},
		{"SIGKILL", syscall.SIGKILL},
		{"the zero signal", 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := proc.Signal(c.sig).Int(); got != int(c.sig) {
			t.Errorf("Int() = %d, want %d", got, int(c.sig))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// An unmapped signal must stay legible rather than print as a name it does not
// have: "signal 0" tells an operator what arrived, where an empty string or a
// wrong name would not.
func Test_Signal_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		sig  proc.Signal
		want string
	}
	tests := []tc{
		{"the zero signal is not a name", proc.Signal(0), "signal 0"},
		{"SIGTERM", proc.Signal(syscall.SIGTERM), "SIGTERM"},
		{"SIGINT", proc.Signal(syscall.SIGINT), "SIGINT"},
		{"an out-of-range number stays legible", proc.Signal(250), "signal 250"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.sig.String(); got != c.want {
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

// Known is the guard before trusting a decoded value; the zero signal is the
// reserved sentinel and must never read as known.
func Test_Signal_Known(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		sig  proc.Signal
		want bool
	}
	tests := []tc{
		{"the zero signal is reserved", proc.Signal(0), false},
		{"SIGTERM", proc.Signal(syscall.SIGTERM), true},
		{"SIGINT", proc.Signal(syscall.SIGINT), true},
		{"an out-of-range number", proc.Signal(250), false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.sig.Known(); got != c.want {
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
