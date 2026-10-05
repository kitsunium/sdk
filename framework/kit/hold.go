package kit

import (
	"context"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// holdSubject is HoldSubject's body: decl_gen.go writes HoldSubject, from the
// design, as one call of it.
func holdSubject(ctx context.Context, reason string, ids ...string) error {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.HoldSubject(ctx, reason, ids...)
}
