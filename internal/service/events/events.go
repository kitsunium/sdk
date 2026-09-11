// Package events — hosts the engine: registration, removal, and the
// copy-on-write membership the dispatch reads.
package events

import (
	"reflect"

	corev "github.com/kitsunium/sdk/internal/core/events"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// bus is the concrete core/events.Bus. It is unexported: there is one
// canonical in-process dispatch discipline, so a registry would have exactly
// one entry and would add a way to misconfigure it at runtime (the proc,
// resilience, scheduler and lifecycle precedents — ADR 0016 / 0026 / 0041 /
// 0050).
type bus struct {
	// members is the per-type subscription index, published as one
	// copy-on-write snapshot. Publish reads it with a single atomic load and
	// no allocation; Subscribe and Unsubscribe rebuild it — the right trade
	// for an index written at wiring time and read once per published event.
	// The kernel/topic precedent, and the same primitive.
	members snapshot.Value[state]
}

// New returns an empty in-process event bus.
//
// It takes no configuration, and that is a decision rather than an omission.
// Every knob considered was a CONTRACT and not a setting — whether a listener
// error stops the dispatch, whether a cancelled context abandons it, who may
// halt it — and a bus whose semantics vary by construction is a bus no call
// site can be read against. A Config today would be an empty struct, which
// CLAUDE.md rule 5 refuses; if a genuine knob ever appears it arrives as a
// sibling constructor, which is what ADR 0039 prescribes for a published
// signature that cannot grow.
func New() corev.Bus {
	//: the zero snapshot.Value is usable, as sync.Mutex is; no field to fill.
	return &bus{}
}

// Subscribe registers sub against eventType, refusing anything that could
// never fire.
func (b *bus) Subscribe(eventType corev.EventType, sub corev.SubscriptionValue) error {
	//: validate before touching the membership — a malformed registration
	//: never reaches a published state.
	if err := validateEventType(eventType, "subscribe"); err != nil {
		//: the refusal already says why the type could never match.
		return err
	}
	if err := validateSubscription(sub, eventType); err != nil {
		//: the refusal already names the missing part.
		return err
	}
	duplicate := false
	b.members.Update(func(current *state) *state {
		//: a duplicate name would make every error ambiguous and every
		//: Unsubscribe a coin toss.
		if holds(current, eventType, sub.Name) {
			duplicate = true
			//: returning current is Update's documented way to abort without
			//: republishing anything.
			return current
		}
		//: copy-on-write: a publisher may be ranging over the current list.
		return cloneStateWith(current, eventType, sub)
	})
	//: refuse rather than shadowing the first registration.
	if duplicate {
		//: name both halves of the key so the fix is mechanical.
		return kerrs.Wrap(corev.DuplicateListener, kerrs.WrapParams{},
			kerrs.String("listener", sub.Name), kerrs.String("event", eventType.String()))
	}
	//: registered, in its ordered position.
	return nil
}

// Unsubscribe removes the listener registered under name for eventType.
func (b *bus) Unsubscribe(eventType corev.EventType, name string) error {
	//: the same type rule as Subscribe: a type that could never be registered
	//: can hold nothing to remove.
	if err := validateEventType(eventType, "unsubscribe"); err != nil {
		//: the refusal already says why.
		return err
	}
	found := false
	b.members.Update(func(current *state) *state {
		next, ok := cloneStateWithout(current, eventType, name)
		found = ok
		//: cloneStateWithout returns current unchanged when there is nothing
		//: to remove, which aborts the rewrite.
		return next
	})
	//: silence would leave the caller believing a listener is gone that is
	//: still registered under a name they mistyped.
	if !found {
		//: name both halves of the key, as the duplicate refusal does.
		return kerrs.Wrap(corev.UnknownListener, kerrs.WrapParams{},
			kerrs.String("listener", name), kerrs.String("event", eventType.String()))
	}
	//: removed; the next Publish reads the rewritten membership.
	return nil
}

// holds reports whether a listener is already registered under name for
// eventType in the given membership version.
func holds(current *state, eventType corev.EventType, name string) bool {
	//: nothing has ever been registered on this bus.
	if current == nil {
		//: no name can be taken.
		return false
	}
	//: linear over ONE type's listeners, under the writer lock, at wiring
	//: time — a second index would cost a map per rewrite to save this.
	for _, existing := range current.byType[eventType] {
		//: names are unique per event type, not per bus.
		if existing.Name == name {
			//: taken.
			return true
		}
	}
	//: free.
	return false
}

// validateEventType refuses a type no dispatch could ever match.
//
// The interface case is the one this exists for. Publish resolves an event's
// type with reflect.TypeOf, which returns the DYNAMIC type of the value and
// never an interface type, so a subscription keyed on an interface would be
// accepted, listed, and never called — the inert registration ADR 0031
// removes from this SDK.
func validateEventType(eventType corev.EventType, at string) error {
	//: a nil type has no identity to key on.
	if eventType == nil {
		//: name the call that was refused, since both Subscribe and
		//: Unsubscribe come through here.
		return kerrs.Wrap(corev.InvalidEventType, kerrs.WrapParams{},
			kerrs.String("at", at), kerrs.String("kind", "nil"))
	}
	//: an interface type would never equal a published value's dynamic type.
	if eventType.Kind() == reflect.Interface {
		//: say which type, so the fix is mechanical.
		return kerrs.Wrap(corev.InvalidEventType, kerrs.WrapParams{},
			kerrs.String("at", at), kerrs.String("kind", "interface"),
			kerrs.String("event", eventType.String()))
	}
	//: dispatchable.
	return nil
}

// validateSubscription refuses a subscription that could never run, naming
// the missing part.
func validateSubscription(sub corev.SubscriptionValue, eventType corev.EventType) error {
	//: a nameless listener cannot be reported on, and no Unsubscribe could
	//: ever name it.
	if sub.Name == "" {
		//: the field says which part is missing.
		return kerrs.Wrap(corev.InvalidSubscription, kerrs.WrapParams{},
			kerrs.String("missing", "Name"), kerrs.String("event", eventType.String()))
	}
	//: a nil Listener would panic on the first published event — inside the
	//: publisher's goroutine, which is the one that did nothing wrong.
	if sub.Listener == nil {
		//: refuse at registration, where the caller can still fix it.
		return kerrs.Wrap(corev.InvalidSubscription, kerrs.WrapParams{},
			kerrs.String("missing", "Listener"), kerrs.String("listener", sub.Name),
			kerrs.String("event", eventType.String()))
	}
	//: runnable.
	return nil
}
