package app

import (
	"context"

	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Erasure is what app.Erase did, store by store.
type Erasure = iapp.Erasure

// Erase erases every record whose subject is one of ids, in every store of
// the app, as its retention would — or deletes it where the store says
// app.DeleteOnErasure — and folds the stores it wrote, so that what it
// overwrote leaves their files. A held record is left in place, and the
// result says so: it lists, per store, what was erased, deleted and held.
// Telling the recipients (GDPR art. 19) is the product's job; the register
// lists them. reason is journaled.
//
// Once sealing lands (ADR 0006, step 3), an erasure also moves the held
// records under keys of their own and destroys the subject's data key, so
// that it reaches every copy kit sealed.
func Erase(ctx context.Context, reason string, ids ...string) (Erasure, error) {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Erase(ctx, reason, ids...)
}
