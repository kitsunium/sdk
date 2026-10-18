package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Written is what a watch is told of a write: which store, which record, and
// whether it was deleted — never a value. The watch reads the record itself
// ([RecordsOf]): without its secret members, and journaled.
type Written = iapp.WrittenEvent

// Watch is a subscription fed by stores instead of a topic: its handler is
// told of every write the framework confirms on a store whose entity holds a field
// with its mark. [Service].Watch declares one.
type Watch = iapp.Watch

// Mark is what a module looks for in the product's fields: app.Personal,
// app.Special or app.Moderated. There is no mark for public or secret:
// there is nothing to find in the first, and nothing may watch the second.
type Mark = iapp.Mark

// FieldRef is one field of one store that carries a mark: where the field
// is, and what its tag says of it.
type FieldRef = iapp.FieldRefValue
