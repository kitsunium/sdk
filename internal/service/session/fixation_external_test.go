package session_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	coresession "github.com/kitsunium/sdk/internal/core/session"
	svcsession "github.com/kitsunium/sdk/internal/service/session"
)

// TestLoggingInRotatesTheIdentifier is the attack this domain exists to
// prevent, written as a test.
//
// The attacker's move is to make the victim's browser hold an identifier the
// attacker already knows, then wait for the victim to log in. If the identifier
// survives authentication, the attacker is now logged in as the victim. The
// defence is that authentication IS a rotation, so the identifier the attacker
// planted names nothing the moment the victim signs in.
func TestLoggingInRotatesTheIdentifier(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := f.build(t, clock.NewManualClock(origin))
			//: the identifier the attacker planted and still holds.
			planted, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			//: some anonymous state survives the login — a cart, a locale.
			if saveErr := store.Save(ctx, planted.Set("cart", "2 items")); saveErr != nil {
				t.Fatalf("Save: %v", saveErr)
			}
			//: the victim authenticates. This is the ONLY call that binds a
			//: subject, and it always mints a new identifier.
			elevated, regenErr := store.Regenerate(ctx, planted.ID(), "victim@example.test")
			if regenErr != nil {
				t.Fatalf("Regenerate: %v", regenErr)
			}
			if elevated.ID().Equal(planted.ID()) {
				t.Fatal("Regenerate reused the identifier — this is session fixation")
			}
			if elevated.Subject() != "victim@example.test" {
				t.Errorf("Subject = %q, want the authenticated principal", elevated.Subject())
			}
			//: the data crosses the boundary; that is why Regenerate exists
			//: rather than "destroy and create".
			if value, present := elevated.Get("cart"); !present || value != "2 items" {
				t.Errorf("cart = (%q, %v), want it carried across", value, present)
			}
			//: and the attacker's identifier is dead, immediately.
			if _, loadErr := store.Load(ctx, planted.ID()); !errs.HasCode(loadErr, coresession.CodeNotFound) {
				t.Fatalf("the planted identifier still resolves after login: %v", loadErr)
			}
		})
	}
}

// TestSaveRefusesToRebindASubject pins the runtime half of the fixation guard.
//
// The structural half — no WithSubject, no SetSubject — is asserted in
// core/session's port test. It is not quite enough on its own: StateValue is
// exported so a framework can implement its own Store, so a forged session
// naming any subject IS constructible. What that forgery must not be is
// PERSISTABLE, and this is the test that says so.
func TestSaveRefusesToRebindASubject(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := f.build(t, clock.NewManualClock(origin))
			anonymous, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			//: build the session the attacker wishes existed: same identifier,
			//: a subject it was never given.
			forged, buildErr := coresession.NewSessionValue(coresession.StateValue{
				ID:             anonymous.ID(),
				Subject:        "admin",
				CreatedAt:      anonymous.CreatedAt(),
				LastSeen:       anonymous.LastSeen(),
				AbsoluteExpiry: anonymous.ExpiresAt(),
				IdleExpiry:     anonymous.ExpiresAt(),
			})
			if buildErr != nil {
				t.Fatalf("NewSessionValue: %v", buildErr)
			}
			saveErr := store.Save(ctx, forged)
			if !errs.HasCode(saveErr, coresession.CodeFixationRefused) {
				t.Fatalf("Save(forged subject) = %v, want CodeFixationRefused", saveErr)
			}
			//: refused LOUDLY, not silently reduced to a data-only write: a
			//: silent success would leave the caller believing they had logged
			//: the user in.
			if !errs.HasReason(saveErr, "FIXATION_REFUSED") {
				t.Errorf("Save = %v, want reason FIXATION_REFUSED", saveErr)
			}
			//: and nothing was written.
			reloaded, loadErr := store.Load(ctx, anonymous.ID())
			if loadErr != nil {
				t.Fatalf("Load: %v", loadErr)
			}
			if reloaded.Subject() != "" {
				t.Errorf("the stored subject became %q — the forgery was applied", reloaded.Subject())
			}
		})
	}
}

// TestSavePersistsDataWithoutTouchingTheSubject pins the ordinary path, so the
// refusal above is not simply "Save never works".
func TestSavePersistsDataWithoutTouchingTheSubject(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := f.build(t, clock.NewManualClock(origin))
			fresh, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			bound, regenErr := store.Regenerate(ctx, fresh.ID(), "alice")
			if regenErr != nil {
				t.Fatalf("Regenerate: %v", regenErr)
			}
			if saveErr := store.Save(ctx, bound.Set("locale", "fr").Set("theme", "dark")); saveErr != nil {
				t.Fatalf("Save: %v", saveErr)
			}
			reloaded, loadErr := store.Load(ctx, bound.ID())
			if loadErr != nil {
				t.Fatalf("Load: %v", loadErr)
			}
			if value, _ := reloaded.Get("locale"); value != "fr" {
				t.Errorf("locale = %q, want fr", value)
			}
			if reloaded.Subject() != "alice" {
				t.Errorf("Subject = %q, want alice", reloaded.Subject())
			}
			//: and a Delete round-trips as an absence, not as an empty string.
			if saveErr := store.Save(ctx, reloaded.Delete("theme")); saveErr != nil {
				t.Fatalf("Save: %v", saveErr)
			}
			trimmed, reloadErr := store.Load(ctx, bound.ID())
			if reloadErr != nil {
				t.Fatalf("Load: %v", reloadErr)
			}
			if _, present := trimmed.Get("theme"); present {
				t.Error("the deleted key came back")
			}
		})
	}
}

// TestRotatingDoesNotBuyTime pins the rule that keeps the absolute ceiling from
// being defeated by a caller who rotates on a timer.
func TestRotatingDoesNotBuyTime(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			manual := clock.NewManualClock(origin)
			store := f.build(t, manual)
			fresh, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			bound, regenErr := store.Regenerate(ctx, fresh.ID(), "alice")
			if regenErr != nil {
				t.Fatalf("Regenerate: %v", regenErr)
			}
			ceiling := bound.CreatedAt().Add(absoluteCeiling)
			//: rotate every ten minutes, keeping the same principal.
			current := bound
			for range 9 {
				manual.Advance(10 * time.Minute)
				rotated, rotErr := store.Regenerate(ctx, current.ID(), "alice")
				if rotErr != nil {
					t.Fatalf("Regenerate at %v: %v", manual.Now(), rotErr)
				}
				if rotated.ID().Equal(current.ID()) {
					t.Fatal("Regenerate reused the identifier")
				}
				//: same principal means the ceiling does NOT move.
				if !rotated.CreatedAt().Equal(bound.CreatedAt()) {
					t.Fatalf("CreatedAt moved to %v after a same-subject rotation", rotated.CreatedAt())
				}
				if rotated.ExpiresAt().After(ceiling) {
					t.Fatalf("ExpiresAt = %v, past the original ceiling %v", rotated.ExpiresAt(), ceiling)
				}
				current = rotated
			}
			//: and the session dies at the original ceiling regardless.
			manual.Advance(10 * time.Minute)
			if _, loadErr := store.Load(ctx, current.ID()); !errs.HasCode(loadErr, coresession.CodeExpired) {
				t.Errorf("a session rotated on a timer outlived its ceiling: %v", loadErr)
			}
		})
	}
}

// TestChangingPrincipalRestartsTheClocks pins the other half of the rule: a
// privilege boundary produces a genuinely new session, and handing it the
// remaining seconds of the browsing before it would log a user out moments
// after they signed in.
func TestChangingPrincipalRestartsTheClocks(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			manual := clock.NewManualClock(origin)
			store := f.build(t, manual)
			fresh, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			//: browse anonymously until the ceiling is nearly reached.
			for range 9 {
				manual.Advance(10 * time.Minute)
				if _, loadErr := store.Load(ctx, fresh.ID()); loadErr != nil {
					t.Fatalf("Load: %v", loadErr)
				}
			}
			bound, regenErr := store.Regenerate(ctx, fresh.ID(), "alice")
			if regenErr != nil {
				t.Fatalf("Regenerate: %v", regenErr)
			}
			//: a principal change is a new session: both clocks restart.
			if !bound.CreatedAt().Equal(manual.Now()) {
				t.Errorf("CreatedAt = %v, want the login instant %v", bound.CreatedAt(), manual.Now())
			}
			//: ten minutes past the original ceiling, the new session is fine.
			manual.Advance(20 * time.Minute)
			if _, loadErr := store.Load(ctx, bound.ID()); loadErr != nil {
				t.Errorf("the freshly authenticated session expired on the old clock: %v", loadErr)
			}
		})
	}
}

// TestRegenerateRefusesADeadSession pins that expiry is not quietly undone by
// an authentication: a session that has already lapsed is not re-authenticated,
// it is refused, and the caller starts a new one.
func TestRegenerateRefusesADeadSession(t *testing.T) {
	t.Parallel()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			manual := clock.NewManualClock(origin)
			store := f.build(t, manual)
			fresh, err := store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			manual.Advance(absoluteCeiling)
			_, regenErr := store.Regenerate(ctx, fresh.ID(), "alice")
			if !errs.HasCode(regenErr, coresession.CodeExpired) {
				t.Errorf("Regenerate on an expired session = %v, want CodeExpired", regenErr)
			}
		})
	}
}

// TestConstructorsRefuseAnInertPolicy pins ADR 0031 for this domain. A zero
// timeout read as "expires immediately" would produce a store in which every
// session is already dead — a login loop with no error message anywhere.
func TestConstructorsRefuseAnInertPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		idle     time.Duration
		absolute time.Duration
	}{
		{"no idle timeout", 0, time.Hour},
		{"no absolute timeout", time.Minute, 0},
		{"negative idle", -time.Minute, time.Hour},
		{"idle equals the ceiling", time.Hour, time.Hour},
		{"idle beyond the ceiling", 2 * time.Hour, time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := svcsession.NewMemoryStore(svcsession.Config{
				IdleTimeout: tc.idle, AbsoluteTimeout: tc.absolute,
			})
			if !errs.HasCode(err, coresession.CodeInvalidConfig) {
				t.Fatalf("NewMemoryStore(%v/%v) = %v, want CodeInvalidConfig", tc.idle, tc.absolute, err)
			}
			if store != nil {
				t.Error("a refused constructor returned a store anyway")
			}
			//: the error names the field, so the fix does not need this file.
			if !mentionsAField(errs.FieldsOf(err)) {
				t.Errorf("InvalidConfig carried no field naming the problem: %v", errs.FieldsOf(err))
			}
		})
	}
}

// TestFileStoreRefusesAMissingDirectoryOrKey pins the two extra refusals only a
// persistent store has.
func TestFileStoreRefusesAMissingDirectoryOrKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  svcsession.FileConfig
	}{
		{"no directory", svcsession.FileConfig{
			IdleTimeout: idleWindow, AbsoluteTimeout: absoluteCeiling,
		}},
		{"no key", svcsession.FileConfig{
			IdleTimeout: idleWindow, AbsoluteTimeout: absoluteCeiling, Dir: t.TempDir(),
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := svcsession.NewFileStore(tc.cfg)
			if !errs.HasCode(err, coresession.CodeInvalidConfig) {
				t.Errorf("NewFileStore = %v, want CodeInvalidConfig", err)
			}
		})
	}
}

// mentionsAField reports whether any field carries a non-empty key.
func mentionsAField(fields []errs.FieldValue) bool {
	for _, field := range fields {
		if strings.TrimSpace(field.Key()) != "" {
			return true
		}
	}
	return false
}
