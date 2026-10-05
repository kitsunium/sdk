package sql

import stdsql "database/sql"

// IsZero reports whether the options ask for nothing beyond the driver's
// defaults.
//
// It exists because a NESTED scope is a savepoint, and a savepoint can change
// neither the isolation level nor the read-only-ness of the transaction it
// sits inside. Non-zero options on a nested call are therefore refused with
// [NestedIsolation] rather than ignored — silently downgrading a caller's
// explicit `Serializable` to whatever the outer transaction happened to use is
// the kind of "helpful" behaviour that produces a data race nobody can find
// (ADR 0055 §D5).
func (o TxOptionsValue) IsZero() bool {
	//: LevelDefault is the stdlib's own zero value for IsolationLevel.
	return o.Isolation == stdsql.LevelDefault && !o.ReadOnly
}

// StdOptions renders the value as the stdlib's own option struct, ready for
// (*sql.DB).BeginTx.
func (o TxOptionsValue) StdOptions() *stdsql.TxOptions {
	//: a pointer is what BeginTx takes; nil would also mean "defaults", but
	//: returning a populated struct keeps one code path at the call site.
	return &stdsql.TxOptions{Isolation: o.Isolation, ReadOnly: o.ReadOnly}
}
