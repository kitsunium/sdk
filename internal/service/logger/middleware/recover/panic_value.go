// Package recover: panic_value.go declares the panicValue adapter that
// converts a recover() return value (any) into the error interface so
// errs.Wrap can carry it without losing the original Stringer behaviour.
package recover

import "fmt"

// panicValue adapts a recovered panic value (any) into the error interface
// so errs.Wrap can carry it without losing the original Stringer behaviour.
type panicValue struct {
	// v is the verbatim recover() return value.
	v any
}

// Error renders the panic value via fmt.Sprintf so any Stringer is honoured.
//
// Returns:
//   - msg: a "panic: <stringified value>" rendering of the recover() value.
func (p panicValue) Error() (msg string) {
	//: fmt.Sprintf preserves the original Stringer behaviour of v.
	return fmt.Sprintf("panic: %v", p.v)
}
