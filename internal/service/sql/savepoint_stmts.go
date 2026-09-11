// Package sql — hosts the three rendered statements of one savepoint.
package sql

// savepointStmts groups one savepoint's rendered statements so runNested
// stays under the parameter ceiling and reads as one operation.
type savepointStmts struct {
	// name is the generated identifier, carried into every error field.
	name string
	// undo is the ROLLBACK TO SAVEPOINT statement.
	undo string
	// release is the RELEASE SAVEPOINT statement.
	release string
}
