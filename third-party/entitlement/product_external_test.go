// External test fixtures: the product the black-box suite verifies against.
package entitlement_test

import entitlement "github.com/kitsunium/sdk/third-party/entitlement"

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
