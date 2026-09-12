// External tests for the public selfupdate surface.
package selfupdate_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/selfupdate"
)

// testSource is a release source naming a product the implementation never
// mentioned, so an assertion that happens to match a hard-coded value fails.
var testSource = selfupdate.Source{
	Owner:      "acme",
	StableRepo: "widget-dist",
	DevRepo:    "widget",
	Product:    "widget",
}

// stubFetcher answers every URL with one canned release document, so the
// vendor-key refusal is reached without touching the network.
type stubFetcher struct{ body string }

// Get returns the canned body with a 200, whatever the URL.
func (f stubFetcher) Get(_ string) (*http.Response, error) {
	//: one document serves the metadata call the upgrade path makes first.
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     http.Header{},
	}, nil
}

// TestAServiceWithNoVendorKeyInstallsNothing pins the refusal that makes every
// other guarantee in this package meaningful.
//
// The alternative shape — verify when a key is present, skip when it is not —
// would make the security property depend on a build flag nobody checks, and it
// fails open: a build that lost its key installs anything. This one fails
// closed, and the code says which refusal it is so a caller can tell "rebuild
// from source" from "the release is forged".
func TestAServiceWithNoVendorKeyInstallsNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		latest  string
	}{
		{name: "a newer release is available", version: "v1.0.0", latest: "v9.9.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fetch := stubFetcher{body: `{"tag_name":"` + tt.latest + `","draft":false,"prerelease":false}`}
			svc := selfupdate.NewWithDeps(tt.version, testSource, fetch, nil, nil)

			_, err := svc.Upgrade()
			//: No key means nothing could have authenticated the release.
			if err == nil {
				t.Fatal("Upgrade() = nil error with no vendor key, want a refusal")
			}
			//: The code is what lets a caller tell this from a network failure.
			if !errs.HasCode(err, selfupdate.CodeNoVendorKey) {
				t.Errorf("Upgrade() err = %v, want code %v", err, selfupdate.CodeNoVendorKey)
			}
		})
	}
}

// TestCheckForUpdateRefusesADevBuild pins that a build with no release version
// says so by code rather than by guessing a comparison it cannot make.
func TestCheckForUpdateRefusesADevBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
	}{
		{name: "the empty version", version: ""},
		{name: "the conventional dev version", version: "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := selfupdate.New(tt.version, testSource).CheckForUpdate()
			//: There is no version to compare, so there is no answer to give.
			if !errs.HasCode(err, selfupdate.CodeDevBuild) {
				t.Errorf("CheckForUpdate() err = %v, want code %v", err, selfupdate.CodeDevBuild)
			}
		})
	}
}

// TestTheTwoOptInsAreDistinct pins that authorising an unattended upgrade does
// not also authorise privilege escalation.
//
// They are two decisions — one permits replacing the binary, the other permits
// doing it as root — and a caller who discovers the second only after acting on
// the first is having a bad afternoon.
func TestTheTwoOptInsAreDistinct(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		product string
	}{
		{name: "a single-word product", product: "widget"},
		{name: "a hyphenated product", product: "my-tool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := selfupdate.Source{Product: tt.product}
			//: Two authorisations must never read the same variable.
			if src.AutoUpgradeEnv() == src.SudoOptInEnv() {
				t.Errorf("both authorisations read %q", src.AutoUpgradeEnv())
			}
		})
	}
}
