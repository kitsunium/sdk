// Package level — Leveler port (a live, mutable severity threshold).
package level

// Leveler exposes a single live severity threshold. A gate or sink consults
// Level on each record so the floor can change at runtime, instead of freezing
// a value at construction. Var is the canonical atomic implementation; any
// constant threshold can satisfy the interface by returning a fixed Level.
type Leveler interface {
	// Level reports the current threshold; implementations MUST be safe for
	// concurrent callers reading while another goroutine mutates the value.
	Level() Level
}
