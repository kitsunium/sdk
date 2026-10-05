package events

import (
	"reflect"
)

// EventType identifies the events a subscription reacts to by their concrete
// Go TYPE. It is an alias for reflect.Type, so a caller never converts.
//
// The key is the type and not a name because a name is unchecked: two
// packages can both pick "user.created", a rename silently unhooks every
// listener, and nothing fails until production is quiet. A Go type is minted
// by the compiler, cannot collide across packages, and is renamed by the same
// tool that renames its uses. The price is that an event crossing a process
// boundary has no identity here — which is the frontier in the package
// documentation, not a limitation to work around.
//
// A subscription's EventType MUST be a CONCRETE type. [Bus.Publish] resolves
// an event's type with reflect.TypeOf, which returns the value's DYNAMIC type
// and never an interface type, so a listener registered on an interface could
// never fire. That registration is refused rather than accepted and silently
// starved — see [InvalidEventType].
type EventType = reflect.Type
