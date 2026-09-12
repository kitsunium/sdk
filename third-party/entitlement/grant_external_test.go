package entitlement_test

import (
	"testing"
	"time"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"
)

// TestGrantValue_Expired pins the daemon's ageing rule: memory of a past
// verification must not outlive the roster window, or revocation would never
// reach a long-running process.
func TestGrantValue_Expired(t *testing.T) {
	t.Parallel()

	verified := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{name: "fresh grant is usable", now: verified.Add(time.Minute), want: false},
		{name: "just inside the window is usable", now: verified.Add(entitlement.RosterLifetime - time.Second), want: false},
		{name: "exactly at the boundary is still usable", now: verified.Add(entitlement.RosterLifetime), want: false},
		{name: "one nanosecond past the boundary stops", now: verified.Add(entitlement.RosterLifetime + time.Nanosecond), want: true},
		{name: "past the window it must stop", now: verified.Add(entitlement.RosterLifetime + time.Second), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := entitlement.GrantValue{Subject: sampleUUID, VerifiedAt: verified}
			if got := g.Expired(tt.now); got != tt.want {
				t.Errorf("Expired(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}
