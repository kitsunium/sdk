// Package signal_test — black-box tests for the public signal facade: the
// Parse round-trip contract that holds on every platform.
package signal_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/signal"
)

// Parse and String are inverses on the portable set, which is what lets a
// config file name a signal and a log line report one. The round-trip is
// asserted in both directions: a name that parses must render back to itself,
// and re-parsing that rendering must yield the identical value — a table that
// mapped two names onto one signal would pass the first check and fail this.
func TestParseRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		signal  string
		wantErr bool
	}
	tests := []tc{
		//: SIGTERM/SIGINT/SIGKILL/SIGHUP/SIGQUIT are in every platform table.
		{"SIGTERM", "SIGTERM", false},
		{"SIGINT", "SIGINT", false},
		{"SIGKILL", "SIGKILL", false},
		{"SIGHUP", "SIGHUP", false},
		{"SIGQUIT", "SIGQUIT", false},
		{"an unknown name is refused", "SIGNOPE", true},
		{"an empty name is refused", "", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sig, err := signal.Parse(c.signal)
		if c.wantErr {
			if err == nil {
				t.Fatalf("Parse(%q) accepted an unknown name", c.signal)
			}
			return
		}
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.signal, err)
		}
		//: String must reproduce the canonical name we started from.
		if got := sig.String(); got != c.signal {
			t.Fatalf("String() = %q, want %q", got, c.signal)
		}
		//: and re-parsing that name yields the identical value.
		again, err := signal.Parse(sig.String())
		if err != nil {
			t.Fatalf("Parse(%q) round-trip: %v", sig.String(), err)
		}
		if again != sig {
			t.Fatalf("round-trip = %v, want %v", again, sig)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Parse also accepts spellings that are not the canonical name — a bare
// number, and the name without its SIG prefix. Those are legitimate inputs
// from a config file, but they do NOT round-trip: String renders the canonical
// name, so parsing "9" yields a value that prints as "SIGKILL". Pinning that
// keeps someone from "fixing" String to echo its input.
func TestParseAcceptsNonCanonicalForms(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		input string
		want  string
	}
	tests := []tc{
		{"a bare number", "9", "SIGKILL"},
		{"the name without its prefix", "TERM", "SIGTERM"},
		{"lowercase", "sigterm", "SIGTERM"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sig, err := signal.Parse(c.input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.input, err)
		}
		if got := sig.String(); got != c.want {
			t.Errorf("Parse(%q).String() = %q, want %q", c.input, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
