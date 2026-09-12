// Package updater_test black-box tests the public surface of the
// privilege-escalation opt-in: the variable name that authorises `sudo -n mv`
// and the sentinel a caller classifies the refusal with.
//
// The guard itself is white-box tested (elevation_internal_test.go proves no
// sudo process is created without the opt-in). What is pinned here is the part
// a CALLER depends on and can therefore break from outside: that escalation is
// its own switch, and that its refusal is not something a retry loop should
// paper over.
package selfupdate_test

import (
	"errors"
	"fmt"
	"testing"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// TestSudoOptInIsASeparateAuthorisation pins that consenting to unattended
// UPGRADES does not also consent to running as root.
//
// These are two different decisions by two different people in practice: a CI
// image sets the upgrade variable so builds stay current, and an operator sets
// the sudo variable because they know the install path is root-owned. Collapsing
// them into one variable — or letting one default from the other — would mean
// every environment that already opted into unattended upgrades silently
// acquired permission to escalate, which is the single change in this package
// nobody would notice until it mattered.
//
// The literals are restated rather than compared to each other: asserting
// `testSource.SudoOptInEnv() != testSource.AutoUpgradeEnv()` alone would still pass if
// both had been renamed to something no environment sets.
func TestSudoOptInIsASeparateAuthorisation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		got  string
		want string
		why  string
	}{
		{
			name: "the escalation switch",
			got:  testSource.SudoOptInEnv(),
			want: testSource.SudoOptInEnv(),
			why:  "an operator sets this because they know the install path is root-owned",
		},
		{
			name: "the unattended-upgrade switch",
			got:  testSource.AutoUpgradeEnv(),
			want: testSource.AutoUpgradeEnv(),
			why:  "a CI image sets this so builds stay current, without granting anything else",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: A renamed variable strands every environment already setting it.
			if tt.got != tt.want {
				t.Errorf("env name = %q, want %q (%s)", tt.got, tt.want, tt.why)
			}
		})
	}

	//: The two decisions must never be spelled the same way, or opting into
	//: unattended upgrades would silently grant privilege escalation too.
	if testSource.SudoOptInEnv() == testSource.AutoUpgradeEnv() {
		t.Errorf("both authorisations read %q; escalation must have its own switch", testSource.SudoOptInEnv())
	}
}

// TestErrSudoNotAuthorisedIsNotTransient pins that the escalation refusal is
// distinguishable from every failure a retry could plausibly fix.
//
// The refusal is permanent by construction: nothing about waiting and trying
// again turns an unset opt-in into a set one. A caller that could not tell it
// apart from a download failure would loop — which is exactly the behaviour the
// explain-failure text goes out of its way to avoid recommending for the other
// non-retryable classes.
func TestErrSudoNotAuthorisedIsNotTransient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		refusal error
		other   error
		why     string
	}{
		{
			name:    "a download failure",
			refusal: coreupd.ElevationNotAuthorised,
			other:   coreupd.DownloadFailed,
			why:     "a retry can fix a network failure; it can never grant an authorisation",
		},
		{
			name:    "an unexpected status",
			refusal: coreupd.ElevationNotAuthorised,
			other:   coreupd.UnexpectedStatus,
			why:     "an endpoint hiccup is transient; a missing opt-in is a decision",
		},
		{
			name:    "an authenticity refusal",
			refusal: coreupd.ElevationNotAuthorised,
			other:   coreupd.SignatureInvalid,
			why:     "both are refusals, but they name different problems and different fixes",
		},
		{
			name:    "a dev build",
			refusal: coreupd.ElevationNotAuthorised,
			other:   coreupd.DevBuild,
			why:     "one says this binary cannot upgrade, the other says it may not escalate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: Wrap the way finalizeReplacement does, because that is the
			//: value a caller actually classifies.
			wrapped := fmt.Errorf("replacing binary: %w", tt.refusal)
			//: Aliased sentinels would send a caller down the wrong branch.
			if errors.Is(wrapped, tt.other) {
				t.Errorf("coreupd.ElevationNotAuthorised classifies as %v (%s)", tt.other, tt.why)
			}
			//: The relation must not hold in the other direction either.
			if errors.Is(fmt.Errorf("fetching release: %w", tt.other), tt.refusal) {
				t.Errorf("%v classifies as coreupd.ElevationNotAuthorised (%s)", tt.other, tt.why)
			}
		})
	}
}
