package sql

import stdsql "database/sql"

// isZero is TxOptionsValue.IsZero's body: decl_gen.go writes TxOptionsValue.IsZero, from the
// design, as one call of it.
func (o TxOptionsValue) isZero() bool {
	//: LevelDefault is the stdlib's own zero value for IsolationLevel.
	return o.Isolation == stdsql.LevelDefault && !o.ReadOnly
}

// stdOptions is TxOptionsValue.StdOptions's body: decl_gen.go writes TxOptionsValue.StdOptions, from the
// design, as one call of it.
func (o TxOptionsValue) stdOptions() *stdsql.TxOptions {
	//: a pointer is what BeginTx takes; nil would also mean "defaults", but
	//: returning a populated struct keeps one code path at the call site.
	return &stdsql.TxOptions{Isolation: o.Isolation, ReadOnly: o.ReadOnly}
}
