package statemachine

import "github.com/kitsunium/sdk/internal/kernel/errs"

const (
	// TriggerStart is the creation of an entity: the step into its initial
	// state.
	TriggerStart Trigger = iota + 1
	// TriggerEvent is an event transition, fired by a caller.
	TriggerEvent
	// TriggerDelay is a timer transition due once an entity has spent a
	// declared duration in its state.
	TriggerDelay
	// TriggerDeadline is a timer transition due at an instant the entity
	// itself carries — a due date, an expiry.
	TriggerDeadline
	// TriggerGuard is a transition due as soon as a condition on the entity
	// holds.
	TriggerGuard
)

// triggerNames spells each trigger, indexed by its value; index 0 is the zero
// value's, which has no name.
var triggerNames = [...]string{"", "start", "event", "delay", "deadline", "guard"}

// String returns the trigger's name — "start", "event", "delay", "deadline"
// or "guard" — or "" for a value outside the set, which is how a caller tells
// a trigger from one that is not.
func (t Trigger) String() string {
	//: the zero value and anything past the table have no name.
	if int(t) >= len(triggerNames) {
		//: outside the closed set.
		return ""
	}
	//: the table is indexed by the value.
	return triggerNames[t]
}

// ParseTrigger reads a trigger's name, as String writes it. Anything else —
// an empty name included — is refused with [TriggerUnknown], which does not
// repeat the name.
func ParseTrigger(name string) (Trigger, error) {
	//: one comparison per trigger; the set is five names long.
	for i := TriggerStart; int(i) < len(triggerNames); i++ {
		//: an exact, case-sensitive match only.
		if triggerNames[i] == name {
			//: found.
			return i, nil
		}
	}
	//: a journal written by something else, or corrupted.
	return 0, errs.Wrap(TriggerUnknown, errs.WrapParams{}, errs.Int("length", len(name)))
}
