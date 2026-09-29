// Package kit — watches: how a module hears the writes of a product's marked
// fields.
package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Written is what a watch is told of a write: which store, which record, and
// whether it was deleted — never a value. The watch reads the record itself
// ([RecordsOf]): without its secret members, and journaled.
type Written = ikit.WrittenEvent

// Watch is a subscription fed by stores instead of a topic: its handler is
// told of every write kit confirms on a store whose entity holds a field
// with its mark. [Service].Watch declares one.
type Watch = ikit.Watch

// Mark is what a module looks for in the product's fields: kit.Personal,
// kit.Special or kit.Moderated. There is no mark for public or secret:
// there is nothing to find in the first, and nothing may watch the second.
type Mark = ikit.Mark

// FieldRef is one field of one store that carries a mark: where the field
// is, and what its tag says of it.
type FieldRef = ikit.FieldRefValue
