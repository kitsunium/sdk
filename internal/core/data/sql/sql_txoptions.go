// Package sql — hosts TxOptionsValue, the per-transaction options a
// Transactor honours.
package sql

import stdsql "database/sql"

// TxOptionsValue parameterises ONE transaction. It is a value, not a manager
// setting, because isolation is a property of the unit of work and not of the
// pool it runs on.
//
// The zero value is a working configuration: the driver's default isolation
// level, read-write. That is the only non-arbitrary default available — every
// engine defines its own default level and picking one here would silently
// change the semantics of an existing application on migration (ADR 0031's
// clamp side, where the default needs no explanation because it is the
// database's own).
//
// It is deliberately a struct rather than two extra method parameters so
// [Transactor] stays frozen at one method: a new option is a new FIELD, which
// breaks nobody, where a new method breaks every downstream double (ADR 0039).
type TxOptionsValue struct {
	// Isolation is the transaction isolation level. The zero value
	// (sql.LevelDefault) defers to the driver.
	Isolation stdsql.IsolationLevel
	// ReadOnly asks the engine to refuse writes. Not every driver enforces
	// it; those that do not report an error at Begin rather than accepting
	// writes silently, which is why this is passed through rather than
	// emulated.
	ReadOnly bool
}

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
