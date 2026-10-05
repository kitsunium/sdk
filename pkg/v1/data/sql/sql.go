package sql

import (
	"context"
)

// Transact runs fn inside a transaction with the driver's default isolation.
//
// It is the ergonomic form of [Transactor].Transact for the common case. The
// port itself keeps the options parameter so it never needs a second method
// (ADR 0039); this helper keeps the call site short.
func Transact(ctx context.Context, tm Transactor, fn TxFunc) error {
	//: the zero options are "whatever the database's own default is".
	return tm.Transact(ctx, TxOptions{}, fn)
}
