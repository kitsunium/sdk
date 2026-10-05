package kit

import (
	"context"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// transact is Transact's body: decl_gen.go writes Transact, from the
// design, as one call of it.
func transact(ctx context.Context, fn func(context.Context) error) error {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Transact(ctx, fn)
}
