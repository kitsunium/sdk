package entitlement

import (
	"testing"
	"time"
)

// TestGrantDeadline pins that a grant is bounded by the EARLIEST date in play,
// however many there are, and that a zero date means "not recorded" rather than
// "already expired".
//
// Before NotAfter existed a grant aged against VerifiedAt+RosterLifetime and
// nothing else, so a daemon that verified late in a roster's window kept
// serving for a further 24 hours after that window shut — up to 47 hours
// after the vendor last signed anything, in a scheme whose only dial is a
// 24-hour bound. The subject's own term was ignored the same way.
//
// The bounds became variadic for the same reason, one document further along: a
// CI seat rests on an Actions token as well as on the roster, and two fixed
// parameters could not carry it. The last two rows are that case, and they are
// the ones a fixed-arity signature could not even express.
func TestGrantDeadline(t *testing.T) {
	t.Parallel()

	verified := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name          string
		rosterExpiry  time.Time
		subjectExpiry time.Time
		// extra is a further bound the caller can name — the Actions token's
		// own expiry, for a CI seat. Absent on every device row.
		extra []time.Time
		want  time.Time
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
		{
			//: The CI seat. The Actions token that PROVED the run is a document
			//: the grant rests on, so a thirty-minute proof cannot authorise for
			//: ten hours — which is what ciseat.go did while NotAfter's own
			//: comment said the opposite.
			name:         "a third bound closing soonest wins",
			rosterExpiry: verified.Add(10 * time.Hour),
			extra:        []time.Time{verified.Add(4 * time.Minute)},
			want:         verified.Add(4 * time.Minute),
		},
		{
			//: And it is a BOUND, not the answer: a roster closing before the
			//: token still decides. The tightest wins whichever document it is.
			name:         "a third bound does not beat a nearer roster",
			rosterExpiry: verified.Add(time.Minute),
			extra:        []time.Time{verified.Add(4 * time.Minute)},
			want:         verified.Add(time.Minute),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bounds := append([]time.Time{tt.rosterExpiry, tt.subjectExpiry}, tt.extra...)
			if got := GrantDeadline(verified, bounds...); !got.Equal(tt.want) {
				t.Errorf("GrantDeadline() = %v, want %v", got, tt.want)
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

// TestGrantValueDeadlineResolvesTheFallbackTheFieldCannot pins what a consumer
// needs in order to honour NotAfter WITHOUT polling.
//
// # What was missing
//
// NotAfter's zero value means "no deadline was computed", and the fallback to
// VerifiedAt+RosterLifetime lived inside Expired and nowhere else. A consumer
// reading the FIELD to schedule its next check therefore read the zero instant
// on exactly the grants serve.go produces at start-up, and had no way to derive
// the deadline the SDK would actually apply. Polling was its only option, which
// is how a grant expiring a second after a check keeps authorising until the
// next tick.
//
// The second row is the discriminating one: it is the case a caller could not
// compute for itself, and it fails against any Deadline that merely returns the
// field.
func TestGrantValueDeadlineResolvesTheFallbackTheFieldCannot(t *testing.T) {
	t.Parallel()

	verified := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		// notAfter is what the verification recorded, or the zero instant for
		// a grant seeded from a bare timestamp.
		notAfter time.Time
		want     time.Time
		reason   string
	}{
		{
			name:     "a recorded deadline is the deadline",
			notAfter: verified.Add(2 * time.Hour),
			want:     verified.Add(2 * time.Hour),
			reason:   "the tightest bound the verification found is the answer whenever there is one",
		},
		{
			name:   "an unrecorded deadline resolves to the verification's own lifetime",
			want:   verified.Add(RosterLifetime),
			reason: "this is the value a caller reading the field could not derive, and the reason the method exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			grant := GrantValue{VerifiedAt: verified, NotAfter: tt.notAfter}
			if got := grant.Deadline(); !got.Equal(tt.want) {
				t.Errorf("Deadline() = %s, want %s (%s)",
					got.UTC().Format(time.RFC3339), tt.want.UTC().Format(time.RFC3339), tt.reason)
			}
		})
	}
}

// TestGrantValueExpiredAndDeadlineCannotDisagree is a conservation test over
// the two ways of asking one question.
//
// A consumer schedules on Deadline and the SDK judges with Expired, so the two
// must describe the same instant for every shape of grant. They did not have to:
// the zero-NotAfter fallback was written twice, once in each, which is how a
// field and a method come to disagree about when a grant died. Expired is now
// defined in terms of Deadline, and this is what keeps it that way — a future
// Expired that grew its own copy of the rule fails here rather than in a
// consumer's watchdog.
//
// The probes are one nanosecond either side of the deadline, not an hour: a
// boundary asserted loosely is a boundary that can move by a second and still
// pass.
func TestGrantValueExpiredAndDeadlineCannotDisagree(t *testing.T) {
	t.Parallel()

	verified := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		notAfter time.Time
		reason   string
	}{
		{
			name:     "a grant with a recorded deadline",
			notAfter: verified.Add(2 * time.Hour),
			reason:   "the ordinary shape, produced by every full verification",
		},
		{
			name:   "a grant seeded from a bare timestamp",
			reason: "the shape whose deadline is implied rather than recorded, which is where two copies of the rule would drift",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			grant := GrantValue{VerifiedAt: verified, NotAfter: tt.notAfter}
			deadline := grant.Deadline()

			//: AT the deadline the grant still authorises: Expired is strictly
			//: after, and a consumer waking exactly on its timer must not find
			//: the grant already dead.
			if grant.Expired(deadline) {
				t.Errorf("Expired(Deadline()) = true, want false — a consumer waking on its own "+
					"timer would find the grant already gone (%s)", tt.reason)
			}
			//: One nanosecond later it does not.
			if !grant.Expired(deadline.Add(time.Nanosecond)) {
				t.Errorf("Expired(Deadline()+1ns) = false, want true — Expired is judging a "+
					"different instant from the one Deadline reports (%s)", tt.reason)
			}
			//: One nanosecond earlier it still does authorise, which is the
			//: other side of the same boundary.
			if grant.Expired(deadline.Add(-time.Nanosecond)) {
				t.Errorf("Expired(Deadline()-1ns) = true, want false (%s)", tt.reason)
			}
		})
	}
}
