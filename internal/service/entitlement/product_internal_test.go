// Internal test fixtures: the product every case verifies an entitlement for.
package entitlement

import (
	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// testProduct is the ProductValue the suite injects wherever the source
// implementation read package constants.
//
// Every field deliberately differs from what that implementation hard-coded. A
// fixture reproducing kodflow/ktn/ktn-linter would pass just as well against a
// package that still ignored ProductValue and used its constants; this one
// fails the moment a constant comes back.
var testProduct = ProductValue{
	Name:       "widget",
	CIAudience: "widget-entitlement",
	EnrolURL:   "https://example.invalid/widget/issues/new",
	Origins: []coreent.OriginValue{
		{Name: "primary", BundleURL: "https://example.invalid/widget/roster.signed.json"},
	},
}
