package entitlement_test

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"
)

// TestRequiresUpdate pins the mandatory-update floor, and in particular the
// ASYMMETRY between an unorderable floor and an unorderable current version.
//
// The SemVer cases are the point: string ordering puts "v1.5.9" after
// "v1.5.14", so a lexical comparison would let exactly the builds a floor
// exists to retire keep running.
//
// The three "unidentifiable current" rows are the enforcement half. Each of
// them returned false before, which handed a build that carries a vendor
// anchor — and therefore passes the licence check — a way to ignore the floor
// entirely by reporting a version nobody can order.
func TestRequiresUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		floor   string
		want    bool
		reason  string
	}{
		{name: "below the floor requires an update", current: "v1.5.10", floor: "v1.5.14", want: true, reason: "this is the whole point"},
		{name: "string ordering would get this wrong", current: "v1.5.9", floor: "v1.5.14", want: true, reason: "\"9\" sorts after \"14\" lexically but 9 < 14"},
		{name: "a major behind requires an update", current: "v1.9.9", floor: "v2.0.0", want: true, reason: "major dominates minor and patch"},
		{name: "exactly the floor passes", current: "v1.5.14", floor: "v1.5.14", want: false, reason: "the floor is inclusive"},
		{name: "above the floor passes", current: "v1.6.0", floor: "v1.5.14", want: false, reason: "a candidate build must not be stranded"},
		{name: "an unstamped version is below every floor", current: "dev", floor: "v1.5.14", want: true, reason: "\"dev\" is not a version, and an ANCHORED build reporting it passed the licence check while skipping the floor"},
		{name: "a binary that declared no version is below every floor", current: "", floor: "v1.5.14", want: true, reason: "forgetting WithVersion must not silently disable the floor — license status did exactly that"},
		{name: "no floor recorded blocks nothing", current: "v1.0.0", floor: "", want: false, reason: "a roster predating the field must not lock everyone out"},
		{name: "a malformed floor still fails open", current: "v1.5.10", floor: "not-a-version", want: false, reason: "a publisher typo must not brick every client — this is the half that stays open"},
		{name: "a malformed current version fails closed", current: "banana", floor: "v1.5.14", want: true, reason: "the binary cannot show it clears a bar it cannot name; the FLOOR is well formed here"},
		{name: "a bare version without the v prefix still compares", current: "1.5.10", floor: "v1.5.14", want: true, reason: "the build stamp writes the bare form"},
		{name: "a bare floor without the v prefix still compares", current: "v1.5.10", floor: "1.5.14", want: true, reason: "both spellings must behave alike"},
		{name: "a prerelease below the floor requires an update", current: "v1.5.14-rc.1", floor: "v1.5.14", want: true, reason: "SemVer orders a prerelease before its release"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := entitlement.RequiresUpdate(tt.current, tt.floor); got != tt.want {
				t.Errorf("RequiresUpdate(%q, %q) = %v, want %v (%s)", tt.current, tt.floor, got, tt.want, tt.reason)
			}
		})
	}
}

// TestVerifyEnforcesRequiredVersion pins that the floor travels inside the
// SIGNED roster and is applied by Verify.
//
// That placement is the design: the roster is already fetched on every cold
// start and already authenticated against the compiled-in anchor, so a
// mandatory update cannot be skipped by going offline nor forged by
// redirecting the endpoint. A version read from an unauthenticated API would
// fall to one line in /etc/hosts.
func TestVerifyEnforcesRequiredVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		binary     string
		floor      string
		wantUpdate bool
		reason     string
	}{
		{name: "an out-of-date binary is refused even with a valid licence", binary: "v1.5.10", floor: "v1.5.14", wantUpdate: true, reason: "the subject is entitled; the build is not"},
		{name: "a current binary passes", binary: "v1.5.14", floor: "v1.5.14", wantUpdate: false, reason: "the floor is inclusive"},
		{name: "a roster with no floor blocks nothing", binary: "v1.0.0", floor: "", wantUpdate: false, reason: "absence of the field is not a floor of zero"},
		{name: "a binary that declared no version is refused", binary: "", floor: "v9.9.9", wantUpdate: true, reason: "an undeclared version cannot clear a floor; refusing is the safe direction and the resolving action is an upgrade"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating vendor key: %v", err)
			}

			dir := t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			now := time.Now()

			pair := signPair(t, vendorPriv, entitlement.RosterValue{
				IssuedAt:        now.Add(-time.Hour),
				ExpiresAt:       now.Add(time.Hour),
				Subjects:        map[string]entitlement.SubjectValue{sampleUUID: {Fingerprint: fingerprint}},
				RequiredVersion: tt.floor,
			})

			getter := &multiOriginGetter{
				states:  map[string]originState{"solo": originHealthy},
				current: pair,
			}
			svc := entitlement.NewServiceWithOrigins(getter, dir, vendorPub, testOrigins("solo")).WithVersion(tt.binary)

			_, verifyErr := svc.Verify(now)

			if errors.Is(verifyErr, entitlement.ErrUpdateRequired) != tt.wantUpdate {
				t.Errorf("Verify() error = %v, want ErrUpdateRequired = %v (%s)", verifyErr, tt.wantUpdate, tt.reason)
			}
			//: A binary that meets the floor must be fully authorised, not
			//: merely spared the update check.
			if !tt.wantUpdate && verifyErr != nil {
				t.Errorf("Verify() error = %v, want nil (%s)", verifyErr, tt.reason)
			}
		})
	}
}

// TestUpdateRefusal pins that the refusal answers the only
// question anyone asks at that moment: from what, to what.
func TestUpdateRefusal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		floor   string
	}{
		{name: "both versions appear in the message", current: "v1.5.10", floor: "v1.5.14"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := entitlement.UpdateRefusal(tt.current, tt.floor)

			//: The sentinel must survive wrapping, or the exit-code dispatch
			//: would fall through to a generic licence failure.
			if !errors.Is(err, entitlement.ErrUpdateRequired) {
				t.Errorf("UpdateRefusal() = %v, want it to wrap ErrUpdateRequired", err)
			}
			message := err.Error()
			//: Both versions must be named for the message to be actionable.
			for _, want := range []string{tt.current, tt.floor} {
				if !strings.Contains(message, want) {
					t.Errorf("UpdateRefusal() = %q, want it to mention %q", message, want)
				}
			}
		})
	}
}

// TestUpdateRequiredError_Error pins that the refusal message names both
// versions. It is the one thing a user sees before an automatic upgrade
// starts, and "from what, to what" is the only question they have.
func TestUpdateRequiredError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		current  string
		required string
	}{
		{name: "both versions appear", current: "v1.5.10", required: "v1.5.14"},
		{name: "an unstamped current version still renders", current: "dev", required: "v2.0.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := &entitlement.UpdateRequiredError{Current: tt.current, Required: tt.required}
			message := err.Error()

			//: Both ends must be named for the message to be actionable.
			for _, want := range []string{tt.current, tt.required} {
				if !strings.Contains(message, want) {
					t.Errorf("Error() = %q, want it to mention %q", message, want)
				}
			}
		})
	}
}

// TestUpdateRequiredError_Unwrap pins the tie to the sentinel.
//
// Every exit-code and advice branch in the gate dispatches on errors.Is
// against ErrUpdateRequired. If Unwrap ever stopped returning it, a
// mandatory update would silently fall through to the generic "licence
// verification failed" path — telling the user their licence is broken when
// it is perfectly valid.
func TestUpdateRequiredError_Unwrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
	}{
		{name: "the typed error matches the sentinel", reason: "the gate dispatches on errors.Is"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := &entitlement.UpdateRequiredError{Current: "v1.0.0", Required: "v2.0.0"}

			if !errors.Is(err, entitlement.ErrUpdateRequired) {
				t.Errorf("errors.Is(err, ErrUpdateRequired) = false, want true (%s)", tt.reason)
			}
			//: And it must stay matchable once wrapped, which is how it
			//: reaches the gate through Verify.
			if !errors.Is(fmt.Errorf("verifying: %w", err), entitlement.ErrUpdateRequired) {
				t.Errorf("a wrapped UpdateRequiredError stopped matching the sentinel (%s)", tt.reason)
			}
		})
	}
}
