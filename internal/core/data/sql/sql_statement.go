package sql

// StatementKind says what a [StatementObserver] is told about: an exec, a
// query, a prepare, or one of the three verbs that open and end a
// transaction. The zero value is no kind and is never reported.
type StatementKind uint8

const (
	// StatementExec is a statement that returns no rows.
	StatementExec StatementKind = iota + 1
	// StatementQuery is a statement that returns rows; its rows are counted.
	StatementQuery
	// StatementPrepare is a statement compiled for repeated execution.
	StatementPrepare
	// StatementBegin is a transaction opened.
	StatementBegin
	// StatementCommit is a transaction committed.
	StatementCommit
	// StatementRollback is a transaction rolled back.
	StatementRollback
)

// String returns the kind's lowercase name, "unknown" for none.
func (k StatementKind) String() string {
	switch k {
	//: one name per kind.
	case StatementExec:
		//: an exec.
		return "exec"
	case StatementQuery:
		//: a query.
		return "query"
	case StatementPrepare:
		//: a prepare.
		return "prepare"
	case StatementBegin:
		//: BEGIN.
		return "begin"
	case StatementCommit:
		//: COMMIT.
		return "commit"
	case StatementRollback:
		//: ROLLBACK.
		return "rollback"
	}
	//: the zero value, or a value this package does not define.
	return "unknown"
}
