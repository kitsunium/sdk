package sql

import (
	"context"
)

// transact is Transact's body: decl_gen.go writes Transact, from the
// design, as one call of it.
func transact(ctx context.Context, tm Transactor, fn TxFunc) error {
	//: the zero options are "whatever the database's own default is".
	return tm.Transact(ctx, TxOptions{}, fn)
}
