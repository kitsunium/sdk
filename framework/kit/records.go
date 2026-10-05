package kit

import (
	"context"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

const (
	// Personal marks a field that holds data about a person: personal,
	// special, or a record's subject.
	Personal Mark = ikit.Personal

	// Special marks a field of GDPR art. 9(1) or art. 10.
	Special Mark = ikit.Special

	// Moderated marks content others see and a moderator may act on.
	Moderated Mark = ikit.Moderated
)

// Records is one store's records without their type. Get returns a record
// without its secret members; EraseFields and Delete take a reason and are
// refused on a held record. Each call is a span on the store's node and an
// entry in the privacy journal.
type Records = ikit.RecordsService

// RecordsOf is [App].Records for code the app runs and that does not hold
// it — a module's watch, its endpoints: the records port of the store whose
// node ID is store, in the app ctx runs in. Outside a running app it is an
// [Unavailable] error.
//
//	func Screen(ctx context.Context, w kit.Written) error {
//		recs, err := kit.RecordsOf(ctx, w.Store)
//		…
//		record, err := recs.Get(ctx, w.Key)
func RecordsOf(ctx context.Context, store string) (Records, error) {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.RecordsOf(ctx, store)
}
