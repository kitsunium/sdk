// Package statemachine — hosts Trigger, the closed set of things that fire a
// transition, with its text form, and the contract's one code: range 0.2.56.*
// (ADR 0120 core/statemachine block).
package statemachine

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.56.0 - 0.2.56.255

// CodeTriggerUnknown identifies a name ParseTrigger does not know.
const CodeTriggerUnknown errs.Code = 0x00_02_38_01 // 0.2.56.1

// Trigger says what fired a transition. The set is closed: every transition a
// machine can take is declared one of these ways, and a history names which.
//
// The values are STABLE — a journal may store them as numbers, and none is
// ever renumbered — and the zero value is not a trigger, so a step read from
// a journal that lost the field names no trigger rather than some default.
// String and ParseTrigger give the text form.
type Trigger uint8

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

var (
	// triggerNames spells each trigger, indexed by its value; index 0 is the
	// zero value's, which has no name.
	triggerNames = [...]string{"", "start", "event", "delay", "deadline", "guard"}

	// TriggerUnknown refuses a name that is not one of the five triggers: a
	// journal written by something else, or damaged. The refused name is
	// never repeated — a journal is the caller's data.
	TriggerUnknown = errs.Define(CodeTriggerUnknown, "TRIGGER_UNKNOWN",
		"That is not a state-machine trigger",
		"core/statemachine: a trigger is start, event, delay, deadline or guard; the name given is none of them")
)

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
