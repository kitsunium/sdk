// Package events — hosts SubscriptionValue and the Priority that orders it.
package events

// Priority orders the listeners registered for one event type. LOWER RUNS
// FIRST, as with every ordering comparator in the stdlib, so a listener that
// must observe the event before the others is registered at a negative
// priority and one that must clean up after them at a positive one.
//
// Two subscriptions at the SAME priority run in REGISTRATION order — the
// order their Subscribe calls returned in. That is a promise, not an
// implementation detail: a bus whose ties resolved by map iteration would
// produce a different order on every run of the same program, and a listener
// set that works on Tuesday would be the same code that fails on Wednesday.
//
// Every value is usable and none is reserved, so there is nothing here for
// [Bus.Subscribe] to refuse (ADR 0031: the zero value is the safe default,
// and the refusals this domain does make are in [InvalidSubscription]).
type Priority int

// PriorityNormal is the zero [Priority] and the value a caller with no
// opinion about ordering should leave in place. Negative priorities run
// before it, positive ones after.
const PriorityNormal Priority = 0

// SubscriptionValue is one named listener's registration against one event
// type: what it is called, when it runs relative to its siblings, whether it
// is allowed to stop the dispatch, and what it does.
//
// The zero value is not runnable and is refused by [Bus.Subscribe]
// ([InvalidSubscription]) rather than accepted and silently skipped.
type SubscriptionValue struct {
	// Name identifies the listener in every error field and is the handle
	// [Bus.Unsubscribe] removes it by. It must be non-empty and unique among
	// the subscriptions registered for the same event type.
	Name string
	// Priority orders this listener among the others registered for the same
	// event type. Its zero value, [PriorityNormal], is a working default.
	Priority Priority
	// MayHalt authorises this listener to stop the dispatch by returning
	// [Halt]. Its zero value — false — is the conservative reading: a
	// listener cannot silently prevent its siblings from ever seeing the
	// event.
	//
	// It is a FIELD rather than a capability every listener has because a veto
	// is an authority, and an authority that is not written down is one nobody
	// reviews. Spelled here it is in the diff, in the review and in grep, at
	// the wiring site where the reader is already asking who does what — the
	// same instrument ADR 0031 applies to resilience.HedgeConfig.Idempotent,
	// for the same reason: a precondition the SDK cannot check becomes a
	// required, greppable assertion instead of a comment.
	//
	// A listener that returns [Halt] without this set does NOT stop the
	// dispatch; the refusal is reported as HALT_NOT_PERMITTED and collected
	// with the other listener errors.
	MayHalt bool
	// Listener is the reaction itself. It must be non-nil.
	Listener Listener
}
