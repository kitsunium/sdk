package entitlement

import (
	"testing"
	"time"
)

// TestGrantDeadline pins that a grant is bounded by the EARLIEST of the three
// dates in play, and that a zero date means "not recorded" rather than
// "already expired".
//
// Before NotAfter existed a grant aged against VerifiedAt+RosterLifetime and
// nothing else, so a daemon that verified late in a roster's window kept
// serving for a further 24 hours after that window shut — up to 47 hours
// after the vendor last signed anything, in a scheme whose only dial is a
// 24-hour bound. The subject's own term was ignored the same way.
func TestGrantDeadline(t *testing.T) {
	t.Parallel()

	verified := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name          string
		rosterExpiry  time.Time
		subjectExpiry time.Time
		want          time.Time
	}{
		{
			name: "no other bound recorded leaves the verification lifetime",
			want: verified.Add(RosterLifetime),
		},
		{
			//: The case that motivated the fix: verified one hour before the
			//: roster shuts, the grant must die with the roster, not 23 hours
			//: after it.
			name:         "a roster closing sooner wins",
			rosterExpiry: verified.Add(time.Hour),
			want:         verified.Add(time.Hour),
		},
		{
			name:          "a subject term closing sooner wins",
			subjectExpiry: verified.Add(2 * time.Hour),
			want:          verified.Add(2 * time.Hour),
		},
		{
			name:          "the earliest of the two wins",
			rosterExpiry:  verified.Add(5 * time.Hour),
			subjectExpiry: verified.Add(2 * time.Hour),
			want:          verified.Add(2 * time.Hour),
		},
		{
			//: A roster whose window outlasts the verification lifetime must
			//: not EXTEND the grant past it.
			name:         "a later roster expiry does not extend the grant",
			rosterExpiry: verified.Add(RosterLifetime + 10*time.Hour),
			want:         verified.Add(RosterLifetime),
		},
		{
			//: Zero means "no term recorded" everywhere else in this package.
			//: Minimising over it instead of skipping it would make every
			//: grant expire at the zero time, i.e. born dead.
			name:          "a zero subject term is not an instant expiry",
			rosterExpiry:  verified.Add(6 * time.Hour),
			subjectExpiry: time.Time{},
			want:          verified.Add(6 * time.Hour),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := grantDeadline(verified, tt.rosterExpiry, tt.subjectExpiry); !got.Equal(tt.want) {
				t.Errorf("grantDeadline() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGrantValueExpiredHonoursNotAfter pins that Expired reads the recorded
// deadline when there is one, and falls back to the verification lifetime
// when there is not — the shape serve.go seeds at start-up, with no roster in
// scope.
func TestGrantValueExpiredHonoursNotAfter(t *testing.T) {
	t.Parallel()

	verified := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	deadline := verified.Add(time.Hour)

	tests := []struct {
		name     string
		notAfter time.Time
		now      time.Time
		want     bool
	}{
		{name: "before a recorded deadline is usable", notAfter: deadline, now: deadline.Add(-time.Second), want: false},
		{name: "exactly at a recorded deadline is still usable", notAfter: deadline, now: deadline, want: false},
		{name: "past a recorded deadline stops", notAfter: deadline, now: deadline.Add(time.Nanosecond), want: true},
		{
			//: This is the regression: without NotAfter the grant would have
			//: been usable here, 23 hours after the roster shut.
			name:     "a recorded deadline beats the verification lifetime",
			notAfter: deadline,
			now:      verified.Add(RosterLifetime - time.Hour),
			want:     true,
		},
		{
			name:     "no deadline falls back to the verification lifetime",
			notAfter: time.Time{},
			now:      verified.Add(RosterLifetime - time.Second),
			want:     false,
		},
		{
			//: A zero deadline must not refuse a daemon at the instant it
			//: starts.
			name:     "no deadline does not mean already expired",
			notAfter: time.Time{},
			now:      verified,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := GrantValue{VerifiedAt: verified, NotAfter: tt.notAfter}
			if got := g.Expired(tt.now); got != tt.want {
				t.Errorf("Expired(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}
