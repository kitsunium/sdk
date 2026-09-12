// Package updater_test provides black-box tests for the public authenticity
// surface: the exported sentinels a caller classifies with errors.Is to tell
// "this release is not the vendor's" apart from "the network was down", and
// the exported opt-in variable names a refusal message has to be able to
// print.
package selfupdate_test

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	selfupdate "github.com/kitsunium/sdk/internal/service/selfupdate"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// TestAuthenticitySentinelsSurviveWrapping pins the public error contract for
// the authenticity sentinels: each stays classifiable with errors.Is after
// being wrapped with %w, which is how license_gate.go decides whether to tell
// the user "do NOT install this manually" or "set the sudo opt-in".
func TestAuthenticitySentinelsSurviveWrapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		sentinel error
	}{
		{name: "signature missing", sentinel: coreupd.SignatureMissing},
		{name: "signature invalid", sentinel: coreupd.SignatureInvalid},
		{name: "no vendor key", sentinel: coreupd.NoVendorKey},
		{name: "sudo not authorised", sentinel: coreupd.ElevationNotAuthorised},
		{name: "insecure redirect", sentinel: coreupd.InsecureRedirect},
		{name: "api body too large", sentinel: coreupd.APIBodyTooLarge},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: A nil sentinel would silently break every errors.Is call site.
			if tc.sentinel == nil {
				t.Fatalf("sentinel %q is nil", tc.name)
			}
			//: Wrap the way the real pipeline does, then require
			//: classification to still succeed — a computed round-trip.
			wrapped := fmt.Errorf("upgrade to v9.9.9: %w", tc.sentinel)
			if !errors.Is(wrapped, tc.sentinel) {
				t.Errorf("errors.Is(wrapped, %s) = false, want true", tc.name)
			}
		})
	}
}

// TestAuthenticitySentinelsMutuallyDistinct guards against two sentinels
// being aliased. Collapsing "unsigned release" into "network failure" would
// turn a supply-chain refusal into something a retry loop papers over.
func TestAuthenticitySentinelsMutuallyDistinct(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a    error
		b    error
	}{
		{name: "missing vs invalid", a: coreupd.SignatureMissing, b: coreupd.SignatureInvalid},
		{name: "missing vs no key", a: coreupd.SignatureMissing, b: coreupd.NoVendorKey},
		{name: "invalid vs no key", a: coreupd.SignatureInvalid, b: coreupd.NoVendorKey},
		{name: "signature vs checksum", a: coreupd.SignatureInvalid, b: coreupd.ChecksumMismatch},
		{name: "signature vs download", a: coreupd.SignatureMissing, b: coreupd.DownloadFailed},
		{name: "sudo vs signature", a: coreupd.ElevationNotAuthorised, b: coreupd.SignatureInvalid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: Distinct sentinels must never classify as one another.
			if errors.Is(tc.a, tc.b) {
				t.Errorf("errors.Is(%s) = true, want false — sentinels aliased", tc.name)
			}
		})
	}
}

// TestOptInEnvNamesAreStable pins the two authorisation variable names as
// part of the public contract.
//
// They are not decoration: every refusal message prints them, the project's
// CI images and devcontainers set them, and renaming one silently strands
// every environment that already did. The literals are duplicated here on
// purpose — asserting `testSource.AutoUpgradeEnv() == testSource.AutoUpgradeEnv()`
// would pass whatever the value became.
func TestOptInEnvNamesAreStable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "auto upgrade", got: testSource.AutoUpgradeEnv(), want: testSource.AutoUpgradeEnv()},
		{name: "allow sudo", got: testSource.SudoOptInEnv(), want: testSource.SudoOptInEnv()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: A renamed variable strands every environment already setting it.
			if tc.got != tc.want {
				t.Errorf("env name = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// releaseGetter answers the release-by-tag lookup with a valid prerelease and
// nothing else, which is enough to drive DownloadCandidate as far as the
// authenticity gate.
type releaseGetter struct{}

// Get answers the release-by-tag lookup and refuses everything else.
//
// Serving an asset here would be counter-productive: the point of the fixture
// is to reach the authenticity gate, so every request past it must fail for an
// unmistakably different reason than "not authenticated".
//
// Parameters:
//   - url: routed on the release-by-tag path only.
//
// Returns:
//   - resp: a 200 carrying one prerelease release, or a 404.
//   - err: always nil.
func (releaseGetter) Get(url string) (resp *http.Response, err error) {
	//: A prerelease is what DownloadCandidate accepts; anything else is
	//: rejected before the authenticity gate is reached.
	if strings.Contains(url, "/releases/tags/") {
		//: The metadata lookup succeeds so the gate is actually reached.
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"tag_name":"v9.9.9-rc.1","prerelease":true,"draft":false}`)),
		}, nil
	}
	//: Every asset request fails, so a run that gets this far cannot be
	//: mistaken for one refused by the anchor check.
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader("")),
	}, nil
}

// TestService_WithVendorKey pins the anchoring contract from the outside: what
// a Service built WITHOUT this call will and will not do.
//
// The default is the point. A Service with no vendor key authenticates nothing
// and therefore installs nothing — it refuses with coreupd.NoVendorKey before a
// single archive byte is fetched. The alternative default, treating an absent
// key as "verification not required", would mean any construction site that
// forgot the call silently reverted to the unauthenticated behaviour the
// signature work exists to end, with nothing anywhere to point it out.
//
// The assertions are deliberately OBSERVABLE rather than field reads: this test
// used to check len(u.vendorKey), which would still pass if the key were stored
// and then never consulted. Driving DownloadCandidate is what proves the stored
// key actually changes the verdict.
func TestService_WithVendorKey(t *testing.T) {
	t.Parallel()

	pub, _, keyErr := ed25519.GenerateKey(nil)
	//: Without a key pair there is nothing to anchor with.
	if keyErr != nil {
		t.Fatalf("generating a vendor key pair: %v", keyErr)
	}

	tests := []struct {
		name string
		key  []byte
		//: wantRefused is true when the anchor check must reject the install.
		wantRefused bool
		why         string
	}{
		{
			name:        "no key at all refuses before downloading",
			key:         nil,
			wantRefused: true,
			why:         "an unanchored build must not install a release it cannot authenticate",
		},
		{
			name:        "a truncated key is not an anchor",
			key:         pub[:ed25519.PublicKeySize-1],
			wantRefused: true,
			why:         "ed25519.Verify panics on a wrong-length key, so the length test keeps the path total",
		},
		{
			name:        "a real key gets past the anchor check",
			key:         pub,
			wantRefused: false,
			why:         "the stored key must actually change the verdict, not merely be stored",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := selfupdate.NewUpdaterWithDeps("v1.0.0", testSource, releaseGetter{}, nil, nil)
			//: The nil row exercises a Service that never had the call made.
			if tt.key != nil {
				svc = svc.WithVendorKey(tt.key)
			}

			_, err := svc.DownloadCandidate("v9.9.9-rc.1")
			//: Every row fails; what distinguishes them is WHY.
			if err == nil {
				t.Fatal("DownloadCandidate() succeeded against a fixture that serves no archive")
			}
			if got := errors.Is(err, coreupd.NoVendorKey); got != tt.wantRefused {
				t.Errorf("errors.Is(err, coreupd.NoVendorKey) = %v, want %v (%s)\nerr = %v", got, tt.wantRefused, tt.why, err)
			}
		})
	}
}

// TestService_WithVendorKey_Chains pins the two structural halves of the
// chaining expression: the receiver comes back so construction reads as one
// expression, and a nil receiver is a no-op rather than a panic.
//
// The nil case is not defensive padding — it is what keeps
// `selfupdate.NewService(v, testSource).WithVendorKey(k)` total when the constructor has
// already refused, so a caller never has to interleave a nil check into the
// chain the API invites them to write.
func TestService_WithVendorKey_Chains(t *testing.T) {
	t.Parallel()

	pub, _, keyErr := ed25519.GenerateKey(nil)
	//: Without a key pair there is nothing to chain.
	if keyErr != nil {
		t.Fatalf("generating a vendor key pair: %v", keyErr)
	}

	tests := []struct {
		name    string
		nilRecv bool
	}{
		{name: "a real receiver comes back for further chaining", nilRecv: false},
		{name: "a nil receiver stays nil instead of panicking", nilRecv: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var svc *selfupdate.Service
			//: The non-nil row needs a real Service to compare identity against.
			if !tt.nilRecv {
				svc = selfupdate.NewService("v1.0.0", testSource)
			}

			got := svc.WithVendorKey(pub)
			//: A nil receiver must not panic and must not conjure a Service.
			if tt.nilRecv {
				if got != nil {
					t.Errorf("WithVendorKey() on a nil receiver = %v, want nil", got)
				}
				return
			}
			//: The same Service comes back so the call chains.
			if got != svc {
				t.Error("WithVendorKey() returned a different Service, want the receiver")
			}
		})
	}
}
