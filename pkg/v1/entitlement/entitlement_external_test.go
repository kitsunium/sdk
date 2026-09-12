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
	"net/http"
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

// TestANilProductNeverPanicsAtConstruction pins, through the public surface
// alone, the contract this package documents everywhere and broke in the one
// place it mattered.
//
// Every ProductValue accessor tolerates a nil receiver, and Validate does too.
// The CONSTRUCTORS did not: both read the Origins FIELD, which a method cannot
// guard, so New(identity, vendor, nil) panicked before returning a verifier —
// on the one path that runs when a consumer has configured nothing yet.
//
// A nil product publishes nowhere, so the right outcome is a verifier that
// refuses with RosterUnreachable. That is a very different thing from a panic,
// and it is what a caller can handle.
func TestANilProductNeverPanicsAtConstruction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		build func() *entitlement.Service
	}{
		{
			name:  "New",
			build: func() *entitlement.Service { return entitlement.New(stubIdentity{}, nil, nil) },
		},
		{
			name: "NewWithGetter",
			build: func() *entitlement.Service {
				return entitlement.NewWithGetter(refusingGetter{}, stubIdentity{}, nil, nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: Construction is the step that panicked; recover names it.
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s(…, nil) panicked on a nil product: %v", tt.name, r)
				}
			}()
			service := tt.build()
			//: A constructor that returns nothing is no better than one that
			//: panics — the caller dereferences it one line later.
			if service == nil {
				t.Fatalf("%s(…, nil) = nil service, want a verifier that refuses", tt.name)
			}
			_, err := service.Verify(time.Now())
			//: A product publishing nowhere cannot decide, which is the
			//: documented fallback rather than a refusal.
			if !errors.Is(err, entitlement.ErrRosterUnreachable) {
				t.Errorf("Verify() = %v, want ErrRosterUnreachable — a product that "+
					"names no origin has nowhere to fetch from", err)
			}
		})
	}
}

// refusingGetter answers every fetch with a failure, so the nil-product cases
// above exercise construction rather than the network.
type refusingGetter struct{}

// Get always fails, which is what a verifier with no origins would see anyway.
//
// Parameters:
//   - url: ignored.
//
// Returns:
//   - resp: always nil.
//   - err: always non-nil.
func (refusingGetter) Get(url string) (resp *http.Response, err error) {
	//: Nothing to serve; the case is about construction.
	return nil, errors.New("no network in this test")
}
