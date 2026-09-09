package session_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/core/session"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// origin is the fixed instant every deadline in this file is measured from. No
// test here reads the wall clock, so none of them can be flaky.
var origin = time.Date(2031, 3, 7, 4, 5, 6, 0, time.UTC)

// TestNewSessionRefusesAnUnnamedSession pins that a session that names nothing
// is refused at construction. It could be Loaded by nobody and Saved over
// anything, so it is not a session.
func TestNewSessionRefusesAnUnnamedSession(t *testing.T) {
	t.Parallel()
	built, err := session.NewSessionValue(session.StateValue{Subject: "alice"})
	if !errs.HasCode(err, session.CodeInvalidID) {
		t.Fatalf("NewSessionValue(zero ID) = %v, want CodeInvalidID", err)
	}
	if !built.IsZero() {
		t.Error("NewSessionValue returned a non-zero session alongside its error")
	}
}

// TestTheEarlierDeadlineAlwaysWins is the executable form of the rule the whole
// expiry policy rests on. The two deadlines disagree in both directions here,
// and neither one is ever averaged, preferred by recency, or ignored.
func TestTheEarlierDeadlineAlwaysWins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		absolute time.Duration
		idle     time.Duration
		want     time.Duration
	}{
		{"idle bites first", 12 * time.Hour, 30 * time.Minute, 30 * time.Minute},
		{"ceiling bites first", 10 * time.Minute, 30 * time.Minute, 10 * time.Minute},
		{"they coincide", time.Hour, time.Hour, time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			built := mustSession(t, session.StateValue{
				ID:             mustID(t, sampleRaw()),
				CreatedAt:      origin,
				LastSeen:       origin,
				AbsoluteExpiry: origin.Add(tc.absolute),
				IdleExpiry:     origin.Add(tc.idle),
			})
			if got := built.ExpiresAt(); !got.Equal(origin.Add(tc.want)) {
				t.Errorf("ExpiresAt() = %v, want %v", got, origin.Add(tc.want))
			}
		})
	}
}

// TestNewSessionClampsASlidingWindowPastTheCeiling pins the second half of the
// rule: the clamp lives in the VALUE TYPE, so it holds even for a session a
// third-party Store built without knowing about it.
func TestNewSessionClampsASlidingWindowPastTheCeiling(t *testing.T) {
	t.Parallel()
	ceiling := origin.Add(time.Hour)
	built := mustSession(t, session.StateValue{
		ID:             mustID(t, sampleRaw()),
		CreatedAt:      origin,
		LastSeen:       origin,
		AbsoluteExpiry: ceiling,
		//: a store that forgot to clamp, or never knew it had to.
		IdleExpiry: origin.Add(99 * time.Hour),
	})
	if !built.ExpiresAt().Equal(ceiling) {
		t.Errorf("ExpiresAt() = %v, want the ceiling %v", built.ExpiresAt(), ceiling)
	}
}

// TestLiveAtIsExclusiveAtTheDeadline pins the boundary: a session is dead AT
// its deadline, not one tick after it.
func TestLiveAtIsExclusiveAtTheDeadline(t *testing.T) {
	t.Parallel()
	deadline := origin.Add(time.Hour)
	built := mustSession(t, session.StateValue{
		ID: mustID(t, sampleRaw()), CreatedAt: origin, LastSeen: origin,
		AbsoluteExpiry: deadline, IdleExpiry: deadline,
	})
	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"one nanosecond before", deadline.Add(-time.Nanosecond), true},
		{"exactly at", deadline, false},
		{"one nanosecond after", deadline.Add(time.Nanosecond), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := built.LiveAt(tc.at); got != tc.want {
				t.Errorf("LiveAt(%v) = %v, want %v", tc.at, got, tc.want)
			}
		})
	}
	//: the zero session is never live, whatever instant it is asked about.
	if (session.SessionValue{}).LiveAt(origin) {
		t.Error("the zero session reported itself live")
	}
}

// TestDataMutatorsCopy pins copy-on-write. A value handed to two goroutines
// must not change under either of them, which is the whole reason Set returns
// a session instead of mutating one.
func TestDataMutatorsCopy(t *testing.T) {
	t.Parallel()
	base := mustSession(t, session.StateValue{
		ID: mustID(t, sampleRaw()), CreatedAt: origin, LastSeen: origin,
		AbsoluteExpiry: origin.Add(time.Hour), IdleExpiry: origin.Add(time.Hour),
		Data: map[string]string{"locale": "en"},
	})
	next := base.Set("locale", "fr").Set("theme", "dark")
	//: the receiver is untouched by either call.
	if value, _ := base.Get("locale"); value != "en" {
		t.Errorf("base locale = %q after Set on a copy, want en", value)
	}
	if _, present := base.Get("theme"); present {
		t.Error("base gained a key that was set on a copy")
	}
	//: and the copy carries both writes.
	if value, _ := next.Get("locale"); value != "fr" {
		t.Errorf("copy locale = %q, want fr", value)
	}
	//: Delete copies too.
	trimmed := next.Delete("theme")
	if _, present := next.Get("theme"); !present {
		t.Error("Delete on a copy removed the key from the receiver")
	}
	if _, present := trimmed.Get("theme"); present {
		t.Error("Delete did not remove the key from the copy")
	}
	//: the caller's map is not the session's storage either.
	dumped := next.Data()
	dumped["locale"] = "de"
	if value, _ := next.Get("locale"); value != "fr" {
		t.Errorf("mutating Data()'s result reached the session: locale = %q", value)
	}
}

// TestGetDistinguishesAbsentFromEmpty pins why Get has two returns: a bare ""
// cannot tell "the user has no locale" from "the user's locale is the empty
// string", and a session store is exactly where that distinction gets used.
func TestGetDistinguishesAbsentFromEmpty(t *testing.T) {
	t.Parallel()
	built := mustSession(t, session.StateValue{
		ID: mustID(t, sampleRaw()), Data: map[string]string{"empty": ""},
	})
	if value, present := built.Get("empty"); value != "" || !present {
		t.Errorf(`Get("empty") = (%q, %v), want ("", true)`, value, present)
	}
	if value, present := built.Get("absent"); value != "" || present {
		t.Errorf(`Get("absent") = (%q, %v), want ("", false)`, value, present)
	}
}

// TestKeysAreSorted pins reproducible iteration. Map order would make any
// output built from a session differ run to run, which is the kind of
// non-determinism that only shows up in someone else's CI.
func TestKeysAreSorted(t *testing.T) {
	t.Parallel()
	built := mustSession(t, session.StateValue{
		ID:   mustID(t, sampleRaw()),
		Data: map[string]string{"zeta": "1", "alpha": "2", "mu": "3"},
	})
	got := built.Keys()
	if !slices.IsSorted(got) {
		t.Errorf("Keys() = %v, want sorted", got)
	}
	if len(got) != 3 {
		t.Errorf("Keys() length = %d, want 3", len(got))
	}
}

// TestSessionRendersItsShapeNeverItsContents is the regression guard for the
// claim that a %v on a session cannot leak the subject, the identifier, or the
// caller's data.
func TestSessionRendersItsShapeNeverItsContents(t *testing.T) {
	t.Parallel()
	id := mustID(t, sampleRaw())
	built := mustSession(t, session.StateValue{
		ID: id, Subject: "user-42-alice@example.test",
		CreatedAt: origin, LastSeen: origin,
		AbsoluteExpiry: origin.Add(time.Hour), IdleExpiry: origin.Add(time.Hour),
		Data: map[string]string{"cart_total": "1499", "email": "alice@example.test"},
	})
	forbidden := []string{
		id.Reveal(), "alice", "user-42", "cart_total", "1499", "example.test",
	}
	for _, verb := range []string{"%v", "%s", "%#v", "%+v"} {
		rendered := fmt.Sprintf(verb, built)
		for _, secret := range forbidden {
			if strings.Contains(rendered, secret) {
				t.Errorf("%s leaked %q: %s", verb, secret, rendered)
			}
		}
		//: what it SHOULD say: bound-or-anonymous, a key count, a deadline.
		if !strings.Contains(rendered, "bound") || !strings.Contains(rendered, "keys:2") {
			t.Errorf("%s = %s, want the shape summary", verb, rendered)
		}
	}
	//: an anonymous session says so without naming anything either.
	anon := mustSession(t, session.StateValue{ID: id})
	if !strings.Contains(fmt.Sprint(anon), "anon") {
		t.Errorf("anonymous session rendered %s, want anon", anon)
	}
}

// mustSession builds a SessionValue or fails the test.
func mustSession(t *testing.T, state session.StateValue) session.SessionValue {
	t.Helper()
	built, err := session.NewSessionValue(state)
	if err != nil {
		t.Fatalf("NewSessionValue: %v", err)
	}
	return built
}
