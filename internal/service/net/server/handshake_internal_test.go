package server

import (
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// handshakeBudgetCase is one group configuration and the bound it must get.
type handshakeBudgetCase struct {
	// name describes the configuration.
	name string
	// configured is the group's Handshake timeout; zero means unset.
	configured time.Duration
	// want is the bound the negotiation must run under.
	want time.Duration
}

// TestHandshakeBudgetDefaults pins that an unconfigured group still bounds its
// handshake.
//
// Read, Write and Idle all treat zero as "no bound", and that is deliberate for
// them: a handler is running and can decide for itself. The handshake is the
// one phase where nothing of the sort is true — it completes before any handler
// exists — so zero here must fall back to the domain default instead. A group
// that configures nothing is the common case, and it was the unbounded one.
func TestHandshakeBudgetDefaults(t *testing.T) {
	t.Parallel()
	cases := []handshakeBudgetCase{
		{name: "unset falls back to the domain default", configured: 0, want: defaultHandshakeTimeout},
		{name: "negative falls back too", configured: -time.Second, want: defaultHandshakeTimeout},
		{name: "an explicit budget wins", configured: 250 * time.Millisecond, want: 250 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runHandshakeBudgetCase(t, tc)
		})
	}
}

// runHandshakeBudgetCase resolves one configuration and checks the bound.
func runHandshakeBudgetCase(t *testing.T, tc handshakeBudgetCase) {
	t.Helper()
	got := handshakeBudget(corenet.TimeoutsValue{
		Handshake: corenet.DurationValue(tc.configured),
	})
	//: an unbounded handshake is the defect; zero must never survive to here.
	if got != tc.want {
		t.Fatalf("handshakeBudget(%v) = %v, want %v", tc.configured, got, tc.want)
	}
}
