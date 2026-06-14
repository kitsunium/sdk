// Package signal_test — black-box tests for the public signal facade: the
// Parse round-trip contract that holds on every platform.
package signal_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/signal"
)

// TestParseRoundTrip asserts Parse(s.String()) == s for the portable signal set,
// the public round-trip contract of issue #62.
func TestParseRoundTrip(t *testing.T) {
	t.Parallel()

	//: SIGTERM/SIGINT/SIGKILL/SIGHUP/SIGQUIT are in every platform table.
	names := []string{"SIGTERM", "SIGINT", "SIGKILL", "SIGHUP", "SIGQUIT"}

	runCase := func(t *testing.T, name string) {
		t.Helper()
		//: parse the canonical name to a typed value.
		sig, err := signal.Parse(name)
		//: a known signal never errors on parse.
		if err != nil {
			t.Fatalf("Parse(%q): %v", name, err)
		}
		//: String must reproduce the canonical name we started from.
		if got := sig.String(); got != name {
			t.Fatalf("String() = %q, want %q", got, name)
		}
		//: and re-parsing that name yields the identical value (full round-trip).
		again, err := signal.Parse(sig.String())
		//: the second parse cannot fail for a name we just produced.
		if err != nil {
			t.Fatalf("Parse(%q) round-trip: %v", sig.String(), err)
		}
		//: identity closes the loop: name out, same signal back in.
		if again != sig {
			t.Fatalf("round-trip = %v, want %v", again, sig)
		}
	}

	for _, name := range names {
		//: subtest per name pins a regression to one signal.
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runCase(t, name)
		})
	}
}
