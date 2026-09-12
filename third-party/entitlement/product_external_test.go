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

// TestANilProductDoesNotPanic pins the nil-receiver contract the package
// documents for every ProductValue method.
//
// GenerateKeyPair dereferenced p.Name directly, so a nil product panicked on the
// one call an operator makes when they have nothing else set up yet.
func TestANilProductDoesNotPanic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(p *entitlement.ProductValue) any
	}{
		{name: "AutoUpgradeEnv-equivalent: the cache directory", call: func(p *entitlement.ProductValue) any { return p.DefaultCacheDir() }},
		{name: "Validate", call: func(p *entitlement.ProductValue) any { return p.Validate() }},
		{name: "IssueURL", call: func(p *entitlement.ProductValue) any { return p.IssueURL("s", "k", false) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: A nil product must reach the documented fallback, never a panic:
			//: this is the one path that runs when nothing else is working.
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panicked on a nil product: %v", r)
				}
			}()
			tt.call(nil)
		})
	}
}

// TestGenerateKeyPairOnANilProduct pins the same contract on the call the
// finding named, which is the only one that writes to disk.
func TestGenerateKeyPairOnANilProduct(t *testing.T) {
	t.Parallel()

	tests := []struct{ name string }{{name: "a valid subject, no product"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if r := recover(); r != nil {
					t.Errorf("GenerateKeyPair panicked on a nil product: %v", r)
				}
			}()
			var product *entitlement.ProductValue
			//: A canonical v4 UUID, so the refusal cannot come from the subject.
			//: The outcome is not what this asserts — only that the call
			//: RETURNED rather than panicking — so the error is reported for
			//: the record and never failed on.
			_, genErr := product.GenerateKeyPair(t.TempDir(), "6ba7b810-9dad-41d1-80b4-00c04fd430c8")
			t.Logf("GenerateKeyPair on a nil product returned: %v", genErr)
		})
	}
}
