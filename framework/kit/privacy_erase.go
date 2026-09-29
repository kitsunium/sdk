// Package kit — erasure: a record's personal data removed as its retention
// would.
package kit

import (
	"context"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Erasure is what kit.Erase did, store by store.
type Erasure = ikit.Erasure

// Erase erases every record whose subject is one of ids, in every store of
// the app, as its retention would — or deletes it where the store says
// kit.DeleteOnErasure — and folds the stores it wrote, so that what it
// overwrote leaves their files. A held record is left in place, and the
// result says so: it lists, per store, what was erased, deleted and held.
// Telling the recipients (GDPR art. 19) is the product's job; the register
// lists them. reason is journaled.
//
// Once sealing lands (ADR 0006, step 3), an erasure also moves the held
// records under keys of their own and destroys the subject's data key, so
// that it reaches every copy kit sealed.
func Erase(ctx context.Context, reason string, ids ...string) (Erasure, error) {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Erase(ctx, reason, ids...)
}
