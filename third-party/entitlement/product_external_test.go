// External test fixtures: the product the black-box suite verifies against.
package entitlement_test

import (
	"os"
	"testing"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"
)

// testProduct is the ProductValue the external suite injects. Every field
// differs from what the source implementation hard-coded, so an assertion that
// happens to match a reintroduced constant fails rather than passes.
var testProduct = entitlement.ProductValue{
	Name:       "widget",
	CIAudience: "widget-entitlement",
	EnrolURL:   "https://example.invalid/widget/issues/new",
	Origins: []entitlement.OriginValue{
		{Name: "primary", BundleURL: "https://example.invalid/widget/roster.signed.json"},
	},
}

// TestRedundancyCountsHostsNotStrings pins the two normalisations the host
// count depends on, each of which let a single point of failure pass as
// redundant before it was a rule.
func TestRedundancyCountsHostsNotStrings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		origins  []entitlement.OriginValue
		wantFail bool
		reason   string
	}{
		{
			name: "two ports on one host are one host",
			origins: []entitlement.OriginValue{
				{Name: "a", BundleURL: "https://one.example.invalid/roster.signed.json"},
				{Name: "b", BundleURL: "https://one.example.invalid:8443/roster.signed.json"},
			},
			wantFail: true,
			reason:   "one machine, one outage — the port does not make it two",
		},
		{
			name: "casing is not a second host",
			origins: []entitlement.OriginValue{
				{Name: "a", BundleURL: "https://One.Example.Invalid/roster.signed.json"},
				{Name: "b", BundleURL: "https://one.example.invalid/roster.signed.json"},
			},
			wantFail: true,
			reason:   "DNS is case-insensitive",
		},
		{
			name: "two unfetchable entries are not two hosts",
			origins: []entitlement.OriginValue{
				{Name: "a", BundleURL: "one"},
				{Name: "b", BundleURL: "two"},
			},
			wantFail: true,
			reason:   "neither can ever be fetched, so neither is an origin",
		},
		{
			name: "a non-http scheme is not an origin",
			origins: []entitlement.OriginValue{
				{Name: "a", BundleURL: "https://one.example.invalid/roster.signed.json"},
				{Name: "b", BundleURL: "file:///etc/roster.signed.json"},
			},
			wantFail: true,
			reason:   "the fetch is HTTP; a file URL is not a second publication path",
		},
		{
			name: "two real hosts still pass",
			origins: []entitlement.OriginValue{
				{Name: "a", BundleURL: "https://one.example.invalid/roster.signed.json"},
				{Name: "b", BundleURL: "https://two.example.invalid/roster.signed.json"},
			},
			reason: "independent hosts are the property being asserted",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			product := entitlement.ProductValue{Name: "p", Origins: tt.origins}
			err := product.Validate()
			//: Redundancy is a property of HOSTS, never of URL strings.
			if (err != nil) != tt.wantFail {
				t.Errorf("Validate() error = %v, wantFail %t (%s)", err, tt.wantFail, tt.reason)
			}
		})
	}
}

// TestGenerateKeyPairRefusesAPathTraversal pins that a subject is validated
// before any path is built from it.
//
// Every other entry point into this package's key paths checks the subject
// first; GenerateKeyPair did not, so "../authorized_keys" wrote both halves of a
// key pair outside the directory the caller named.
func TestGenerateKeyPairRefusesAPathTraversal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		subject string
	}{
		{name: "a parent-directory traversal", subject: "../authorized_keys"},
		{name: "an absolute path", subject: "/etc/passwd"},
		{name: "a plain name that is not a UUID", subject: "subject"},
		{name: "the empty subject", subject: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			product := entitlement.ProductValue{Name: "widget"}
			_, err := product.GenerateKeyPair(dir, tt.subject)
			//: A subject that is not a canonical v4 UUID names no key pair.
			if err == nil {
				t.Fatalf("GenerateKeyPair(%q) = nil error, want a refusal", tt.subject)
			}
			//: And nothing may have been written anywhere.
			entries, readErr := os.ReadDir(dir)
			if readErr == nil && len(entries) != 0 {
				t.Errorf("GenerateKeyPair(%q) wrote %d entries, want none", tt.subject, len(entries))
			}
		})
	}
}
