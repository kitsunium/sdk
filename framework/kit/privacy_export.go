// Package kit — export: a person's data as kit gives it back.
package kit

import (
	"context"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// PersonalData is a person's data as kit.Export gives it.
type PersonalData = ikit.PersonalData

// Export returns every record whose subject is one of ids, in every store of
// the app: each without its secret members, with the former values its
// fields keep (ADR 0007) — never a secret field's —, and per store what
// GDPR art. 15(1) asks beside the data — the purpose, the retention and the
// recipients inside the product. A held record is exported like any other.
// The result is JSON, structured and machine-readable (art. 20); keeping the
// month of art. 12(3) is the product's job, and so is deciding who may call
// it.
//
// A person known under several identities — a user's ID in one store, an
// e-mail address in another — is found by giving all of them. ctx must run
// inside the app: an endpoint's, a job's, a loop's.
func Export(ctx context.Context, ids ...string) (PersonalData, error) {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Export(ctx, ids...)
}
