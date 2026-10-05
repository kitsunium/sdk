package kit

import (
	"context"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// HoldSubject places a legal hold on every record whose subject is one of
// ids, in every store of the app: an authority's request about a person.
// It holds the records they have now; a record written later is not held.
// reason is kept as Store.Hold keeps it.
func HoldSubject(ctx context.Context, reason string, ids ...string) error {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.HoldSubject(ctx, reason, ids...)
}
