// Package validation — hosts planKey, the identity of a compiled plan.
package validation

import "reflect"

// planKey identifies a compiled plan. The MODE is part of the key because
// stop-at-first is compiled INTO the plan — including into its nested plans —
// rather than applied as a truncation afterwards, which would claim a saving
// it did not make. Two modes of one type are therefore two plans, which is
// the honest arrangement: they really do different work.
type planKey struct {
	typ         reflect.Type
	stopAtFirst bool
}
