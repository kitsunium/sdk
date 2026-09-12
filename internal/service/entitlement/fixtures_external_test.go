// External test fixtures shared by the mechanism's black-box suite.
package entitlement_test

import (
	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
	svcent "github.com/kitsunium/sdk/internal/service/entitlement"
)

// sampleUUID is the subject identifier the suite enrols and looks up.
//
// It is a canonical v4 UUID because that is what validSubject accepts, and a
// fixed one because a random subject makes a failure irreproducible.
const sampleUUID string = "11111111-2222-3333-4444-555555555555"

// testProduct is the ProductValue the black-box suite injects wherever the
// source implementation read package constants.
//
// Every field deliberately differs from what that implementation hard-coded: a
// fixture reproducing the real vendor would pass just as well against a package
// that still ignored ProductValue.
var testProduct = svcent.ProductValue{
	Name:       "widget",
	CIAudience: "widget-entitlement",
	EnrolURL:   "https://example.invalid/widget/issues/new",
	Origins: []coreent.OriginValue{
		{Name: "primary", BundleURL: "https://example.invalid/widget/roster.signed.json"},
	},
}
