// Package entitlement_test exercises the facade the way a consumer does:
// through the public package alone, with no access to the internal layers.
//
// That restriction is the point. The facade shipped with fifteen Code
// constants and NOT ONE sentinel, so a consumer could read a refusal's code but
// could not write errors.Is — which is how every caller in the wild actually
// asks. A test importing the internal packages would have passed anyway,
// because the symbols exist there; only a test confined to the public surface
// can fail on a facade that does not export enough to be used.
package entitlement_test

import (
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/entitlement"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// stubIdentity answers the port without touching any key material, so the
// refusals under test come from the roster path and never from the machine.
type stubIdentity struct {
	subject string
	err     error
}

// Discover reports the subject this stub claims to be.
//
// Parameters: none.
//
// Returns:
//   - subject: the configured subject.
//   - err: the configured discovery failure, if any.
func (s stubIdentity) Discover() (subject string, err error) {
	//: Hand back whatever the case configured.
	return s.subject, s.err
}

// Fingerprint reports a fixed fingerprint for any subject.
//
// Parameters:
//   - subject: ignored; this stub holds one identity.
//
// Returns:
//   - fingerprint: a fixed value.
//   - err: always nil.
func (s stubIdentity) Fingerprint(subject string) (fingerprint string, err error) {
	//: A stable value; no test here compares it against a roster.
	return "SHA256:stub", nil
}

// ProvePossession always succeeds, so a refusal is never this stub's doing.
//
// Parameters:
//   - subject: ignored.
//
// Returns:
//   - err: always nil.
func (s stubIdentity) ProvePossession(subject string) error {
	//: Possession is not what any case here is about.
	return nil
}

// TestTheFacadeCarriesBothWaysOfAskingWhy pins that every sentinel the facade
// re-exports carries the Code the facade publishes beside it.
//
// Two spellings of one question have to agree, or a consumer that switched from
// errors.Is to errs.HasCode would silently stop matching. Pairing them in a
// table is what makes a mismatched re-export a failure rather than a surprise
// three releases later.
func TestTheFacadeCarriesBothWaysOfAskingWhy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sentinel error
		code     errs.Code
	}{
		{name: "no licence", sentinel: entitlement.ErrNoLicense, code: entitlement.CodeNoLicence},
		{name: "roster unsigned", sentinel: entitlement.ErrRosterUnsigned, code: entitlement.CodeRosterUnsigned},
		{name: "roster stale", sentinel: entitlement.ErrRosterStale, code: entitlement.CodeRosterStale},
		{name: "revoked", sentinel: entitlement.ErrRevoked, code: entitlement.CodeRevoked},
		{name: "licence expired", sentinel: entitlement.ErrLicenseExpired, code: entitlement.CodeLicenceExpired},
		{name: "key mismatch", sentinel: entitlement.ErrKeyMismatch, code: entitlement.CodeKeyMismatch},
		{name: "roster unreachable", sentinel: entitlement.ErrRosterUnreachable, code: entitlement.CodeRosterUnreachable},
		{name: "ambiguous licence", sentinel: entitlement.ErrAmbiguousLicense, code: entitlement.CodeAmbiguousLicence},
		{name: "CI unverifiable", sentinel: entitlement.ErrCIUnverifiable, code: entitlement.CodeCIUnverifiable},
		{name: "CI unknown key", sentinel: entitlement.ErrCIUnknownKey, code: entitlement.CodeCIUnknownKey},
		{name: "CI not entitled", sentinel: entitlement.ErrCINotEntitled, code: entitlement.CodeCINotEntitled},
		{name: "no possession", sentinel: entitlement.ErrNoPossession, code: entitlement.CodeNoPossession},
		{name: "clock regressed", sentinel: entitlement.ErrClockRegressed, code: entitlement.CodeClockRegressed},
		{name: "update required", sentinel: entitlement.ErrUpdateRequired, code: entitlement.CodeUpdateRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: A nil sentinel would make errors.Is answer true for everything,
			//: which is worse than not exporting it at all.
			if tt.sentinel == nil {
				t.Fatalf("%s: the facade exports a nil sentinel", tt.name)
			}
			got, ok := errs.CodeOf(tt.sentinel)
			//: An untyped sentinel carries no code, so errs.HasCode could never
			//: answer on it — the half of the contract errors.Is cannot cover.
			if !ok {
				t.Fatalf("errs.CodeOf(%v) reports no code: the facade re-exported "+
					"an untyped sentinel", tt.sentinel)
			}
			//: The sentinel and the Code beside it must describe one refusal.
			if got != tt.code {
				t.Errorf("errs.CodeOf(%v) = %v, want %v — the sentinel and the "+
					"Code the facade publishes beside it disagree", tt.sentinel, got, tt.code)
			}
		})
	}
}

// TestAConsumerCanTellCannotDecideFromDecidedNo is the facade's whole reason for
// exporting sentinels, exercised end to end through the public surface.
//
// A verifier with no origins cannot reach anything, and the refusal must read as
// "cannot decide" rather than "not entitled" — through errors.Is AND through
// errs.HasCode, because a consumer will use one or the other and both have to
// answer.
func TestAConsumerCanTellCannotDecideFromDecidedNo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity entitlement.Identity
		reason   string
	}{
		{
			name:     "no origin answers",
			identity: stubIdentity{subject: "6ba7b810-9dad-41d1-80b4-00c04fd430c8"},
			reason:   "nothing to fetch a roster from, and nothing cached",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: A product naming no origin, so the fetch has nowhere to go.
			service := entitlement.New(tt.identity, nil, new(entitlement.Product))
			_, err := service.Verify(time.Now())
			//: The point of the case: this must refuse, not succeed.
			if err == nil {
				t.Fatalf("Verify() = nil error, want a refusal (%s)", tt.reason)
			}
			//: The spelling a caller in the wild reaches for first.
			if !errors.Is(err, entitlement.ErrRosterUnreachable) {
				t.Errorf("errors.Is(err, ErrRosterUnreachable) = false for %v — a "+
					"consumer cannot tell an outage from a revocation (%s)", err, tt.reason)
			}
			//: And the SDK's own spelling, which must agree.
			if !errs.HasCode(err, entitlement.CodeRosterUnreachable) {
				t.Errorf("errs.HasCode(err, CodeRosterUnreachable) = false for %v (%s)",
					err, tt.reason)
			}
		})
	}
}

// TestTheVersionFloorRefusalNamesBothVersions pins that a caller can read the
// required version OUT of the refusal.
//
// Without it the caller upgrades, is refused again for the same reason, and
// loops — which is what the end-to-end suite did before UpdateRequiredError
// existed. The type has to survive the facade for that to keep being true.
func TestTheVersionFloorRefusalNamesBothVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		floor   string
		want    bool
	}{
		{name: "below the floor", current: "v1.0.0", floor: "v1.2.0", want: true},
		{name: "at the floor", current: "v1.2.0", floor: "v1.2.0", want: false},
		{name: "above the floor", current: "v1.3.0", floor: "v1.2.0", want: false},
		{name: "no floor requires nothing", current: "v1.0.0", floor: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: The predicate a caller gates the upgrade on.
			if got := entitlement.RequiresUpdate(tt.current, tt.floor); got != tt.want {
				t.Fatalf("RequiresUpdate(%q, %q) = %v, want %v", tt.current, tt.floor, got, tt.want)
			}
			//: Only the refusing case carries a refusal to inspect.
			if !tt.want {
				return
			}
			err := entitlement.UpdateRefusal(tt.current, tt.floor)
			//: The typed half — the caller reads the floor off it.
			var required *entitlement.UpdateRequiredError
			if !errors.As(err, &required) {
				t.Fatalf("UpdateRefusal(%q, %q) = %v, want an *UpdateRequiredError",
					tt.current, tt.floor, err)
			}
			//: The floor must be readable, or the caller loops on the upgrade.
			if required.Required != tt.floor {
				t.Errorf("UpdateRequiredError.Required = %q, want %q", required.Required, tt.floor)
			}
			//: And the sentinel half, so errors.Is answers too.
			if !errors.Is(err, entitlement.ErrUpdateRequired) {
				t.Errorf("errors.Is(err, ErrUpdateRequired) = false for %v", err)
			}
		})
	}
}
