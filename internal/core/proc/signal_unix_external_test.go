//go:build unix

// Package proc_test — Unix-only round-trip coverage for the extended signal
// table (SIGUSR1/SIGUSR2/SIGWINCH/SIGCHLD/…) that the portable test in
// signal_external_test.go cannot reference on non-Unix builds.
package proc_test

import (
	"syscall"
	"testing"

	"github.com/kitsunium/sdk/internal/core/proc"
)

// TestParseRoundTripUnix asserts Parse(s.String()) == s for the Unix-only
// signals beyond the portable subset, completing the issue #62 contract that
// every supported signal round-trips on the platform.
func TestParseRoundTripUnix(t *testing.T) {
	t.Parallel()

	//: the Unix-only spread the portable test cannot name on Windows.
	cases := []proc.Signal{
		proc.Signal(syscall.SIGUSR1),
		proc.Signal(syscall.SIGUSR2),
		proc.Signal(syscall.SIGWINCH),
		proc.Signal(syscall.SIGCHLD),
		proc.Signal(syscall.SIGCONT),
		proc.Signal(syscall.SIGTSTP),
		proc.Signal(syscall.SIGURG),
		proc.Signal(syscall.SIGXCPU),
	}

	runCase := func(t *testing.T, want proc.Signal) {
		t.Helper()
		//: the canonical String form must parse straight back to the same value.
		got, err := proc.Parse(want.String())
		//: a known Unix signal never errors on round-trip.
		if err != nil {
			t.Fatalf("Parse(%q) returned error: %v", want.String(), err)
		}
		//: identity is the contract: name out, same signal back in.
		if got != want {
			t.Fatalf("Parse(%q) = %d, want %d", want.String(), got, want)
		}
	}

	for _, tc := range cases {
		//: subtest per signal pinpoints a failure to one name.
		t.Run(tc.String(), func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
