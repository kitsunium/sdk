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
func TestParseRoundTrip(t *testing.T) {
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
func TestParseForms(t *testing.T) {
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
func TestParseUnknown(t *testing.T) {
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

// TestSignalOS asserts the os.Signal bridge preserves the numeric value.
func TestSignalOS(t *testing.T) {
	t.Parallel()

	s := proc.Signal(syscall.SIGTERM)
	//: the bridge must yield the identical syscall.Signal (os.Signal carrier).
	if got := s.OS(); got != syscall.SIGTERM {
		t.Fatalf("OS() = %v, want %v", got, syscall.SIGTERM)
	}
	//: Int mirrors the underlying platform number.
	if s.Int() != int(syscall.SIGTERM) {
		t.Fatalf("Int() = %d, want %d", s.Int(), int(syscall.SIGTERM))
	}
}

// TestUnknownSignalString asserts an unmapped signal renders legibly.
func TestUnknownSignalString(t *testing.T) {
	t.Parallel()

	s := proc.Signal(0)
	//: the zero/unknown signal must stay legible rather than print as a name.
	if got := s.String(); got != "signal 0" {
		t.Fatalf("String() = %q, want %q", got, "signal 0")
	}
	//: and it must report as not-known.
	if s.Known() {
		t.Fatal("Signal(0).Known() = true, want false")
	}
}
