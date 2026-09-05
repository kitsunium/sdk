//go:build unix

// Package proc — white-box coverage of the Unix signal name table. The
// portable test in signal_external_test.go can only name the signals that
// exist on every GOOS; the whole table is only reachable from inside the
// package on a Unix build.
package proc

import (
	"strconv"
	"strings"
	"testing"
)

// Test_signalNames pins the table against the API that reads it: every entry
// must render as its own name and parse straight back to the same value. Driving
// the assertion off the table itself — rather than a hand-picked list — means a
// signal added to the table cannot arrive untested, which is exactly how the
// portable test drifted from the Unix one before.
func Test_signalNames(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		sig  Signal
		want string
	}
	tests := make([]tc, 0, len(signalNames))
	for sig, name := range signalNames {
		tests = append(tests, tc{name: name, sig: sig, want: name})
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: every listed name must be the canonical SIG* form, not a number.
		if !strings.HasPrefix(c.want, "SIG") {
			t.Fatalf("table entry %d is named %q, want a SIG* form", int(c.sig), c.want)
		}
		if got := c.sig.String(); got != c.want {
			t.Fatalf("Signal(%d).String() = %q, want %q", int(c.sig), got, c.want)
		}
		//: identity is the contract: name out, same signal back in.
		got, err := Parse(c.want)
		if err != nil {
			t.Fatalf("Parse(%q) = %v, want nil", c.want, err)
		}
		if got != c.sig {
			t.Fatalf("Parse(%q) = %d, want %d", c.want, int(got), int(c.sig))
		}
		//: a listed signal is by definition known.
		if !c.sig.Known() {
			t.Errorf("Known() = false for the listed signal %q", c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_signalNames_Coverage pins the spread rather than any one entry: the
// table must carry the POSIX signals a supervisor actually relies on. A table
// trimmed down to what one platform happened to need would compile, pass every
// round-trip above, and quietly lose the ability to name a child's death.
func Test_signalNames_Coverage(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"the graceful stop", "SIGTERM"},
		{"the ungraceful stop", "SIGKILL"},
		{"the reload convention", "SIGHUP"},
		{"a child changed state", "SIGCHLD"},
		{"the two user-defined signals", "SIGUSR1"},
		{"the second user-defined signal", "SIGUSR2"},
		{"the interactive interrupt", "SIGINT"},
		{"the broken pipe", "SIGPIPE"},
	}
	//: index once so each case is a plain lookup.
	byName := make(map[string]Signal, len(signalNames))
	for sig, name := range signalNames {
		byName[name] = sig
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sig, ok := byName[c.want]
		if !ok {
			t.Fatalf("%s is absent from the Unix signal table", c.want)
		}
		//: a signal number of zero would make Kill(0) a liveness probe rather
		//: than the delivery the caller asked for.
		if int(sig) <= 0 {
			t.Errorf("%s maps to %d, want a positive signal number", c.want, int(sig))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: an unlisted number must still render, as a number, so a log line never
	//: loses the signal entirely.
	unknown := Signal(0x7F)
	if got := unknown.String(); !strings.Contains(got, strconv.Itoa(0x7F)) {
		t.Errorf("Signal(127).String() = %q, want it to carry the number", got)
	}
}
