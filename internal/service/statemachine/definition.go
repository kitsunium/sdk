// Package statemachine — hosts MachineSpec, the declaration of a machine: its
// states, its transitions and its hooks, and what a caller reads back from it.
package statemachine

import (
	"context"
	"fmt"
	"slices"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// arrow is one declared transition. Its trigger says which of delay, instant
// and guard is set.
type arrow[E any, S comparable] struct {
	// instant is At's function; nil otherwise.
	instant func(E) (time.Time, bool)
	// guard is When's function; nil otherwise.
	guard func(E) bool
	// event is the transition's name.
	event string
	// delay is After's duration; zero otherwise.
	delay time.Duration
	// from and to are the states left and entered.
	from, to S
	// trigger is how the transition was declared.
	trigger corestm.Trigger
}

// MachineSpec declares a state machine over entities of type E whose state is
// an S: the state a new entity enters, the transitions between states and how
// each is fired, and the hooks that run on the way.
//
// A MachineSpec is DATA. Every transition is declared before a machine runs,
// which is what lets a caller draw the machine exactly — [MachineSpec.States]
// and [MachineSpec.Transitions] read it back — and what lets the engine know,
// for any state, which timers and guards can move an entity out of it.
//
// Four kinds of transitions:
//
//   - On: an event, fired by a caller with [StateMachine.Fire];
//   - After: a duration spent in a state, fired by the machine's loop;
//   - At: an instant the entity carries — a due date, a deadline — fired by
//     the loop once it has come;
//   - When: a guard on the entity, fired by the loop as soon as it holds. A
//     guard sees the entity only and is asked again each time the entity is
//     written; a condition on the time is an instant, declared with At.
//
// When several automatic transitions leave a state and more than one is due,
// the one declared FIRST fires.
//
// A mistake — a duplicate, an empty event name, a nil function, a duration
// that is not positive — is recorded rather than panicked on, read back with
// [MachineSpec.Problems], and refused by NewStateMachine. Build a MachineSpec on one
// goroutine; once a machine is built from it, the machine keeps its own copy
// and later declarations do not reach it.
type MachineSpec[E any, S comparable] struct {
	// state returns a pointer to the entity's state field.
	state func(*E) *S
	// enter holds the OnEnter hooks, per state, in declaration order.
	enter map[S][]func(context.Context, *E) error
	// initial is the state a new entity enters.
	initial S
	// order lists every state the declaration names, first mention first.
	order []S
	// arrows lists the transitions in declaration order.
	arrows []arrow[E, S]
	// after holds the OnTransition hooks in declaration order.
	after []func(context.Context, ChangeValue[E, S]) error
	// problems lists the declaration's mistakes in the order they were made.
	problems []error
	// hasInitial says Initial was called.
	hasInitial bool
}

// TransitionValue is one declared transition, as [MachineSpec.Transitions]
// reads it back.
type TransitionValue[S comparable] struct {
	// Event is the transition's name.
	Event string
	// From is the state it leaves.
	From S
	// To is the state it enters.
	To S
	// Trigger says how it fires: an event, a delay, a deadline or a guard.
	Trigger corestm.Trigger
	// Delay is After's duration; zero for every other trigger.
	Delay time.Duration
}

// ChangeValue describes one stored transition, as an OnTransition hook
// receives it.
type ChangeValue[E any, S comparable] struct {
	// Entity is the entity as stored after the transition.
	Entity E
	// At is when the transition was stored.
	At time.Time
	// Key is the entity's key in the store.
	Key string
	// Event is the transition's name; core/statemachine.CreateEvent for a
	// creation.
	Event string
	// Actor is who fired it, as Config.Actor read it from the caller's
	// context; empty when the machine's loop fired it.
	Actor string
	// From is the state left: the zero S for a creation.
	From S
	// To is the state entered.
	To S
	// Trigger says what fired it.
	Trigger corestm.Trigger
}

// NewMachineSpec starts the declaration of a machine over entities of type E.
// state returns a pointer to the entity's state field: the engine reads the
// state through it and writes the new state through it, so the store stays
// the single source of truth.
func NewMachineSpec[E any, S comparable](state func(*E) *S) *MachineSpec[E, S] {
	d := &MachineSpec[E, S]{state: state, enter: make(map[S][]func(context.Context, *E) error)}
	//: a nil accessor is a problem to report, not a panic at package init.
	if state == nil {
		d.problem(FunctionMissing, errs.String("function", "state"))
	}
	//: the definition is ready for its declarations.
	return d
}

// Initial sets the state a new entity enters with [StateMachine.Start]. A second
// call replaces the first.
func (d *MachineSpec[E, S]) Initial(state S) *MachineSpec[E, S] {
	d.initial, d.hasInitial = state, true
	d.see(state)
	//: fluent, so a declaration reads as one expression.
	return d
}

// On declares an event transition: [StateMachine.Fire] with event moves an entity
// in state from to state to. One event may leave several states, but only one
// transition per event and state.
func (d *MachineSpec[E, S]) On(event string, from, to S) *MachineSpec[E, S] {
	//: nothing to check beyond what every transition needs.
	return d.add(arrow[E, S]{event: event, from: from, to: to, trigger: corestm.TriggerEvent})
}

// After declares a timer transition: an entity that has spent delay in state
// from moves to state to, fired by the machine's loop. The time is counted
// from when the entity entered the state, which the machine's journal keeps
// across restarts.
func (d *MachineSpec[E, S]) After(event string, delay time.Duration, from, to S) *MachineSpec[E, S] {
	//: a timer due the instant its state is entered reads as a mistake.
	if delay <= 0 {
		d.problem(DelayInvalid, errs.String("event", event))
	}
	//: kept even when refused, so a drawing of the declaration stays whole.
	return d.add(arrow[E, S]{event: event, from: from, to: to, trigger: corestm.TriggerDelay, delay: delay})
}

// At declares a timer transition at an instant the entity carries: an entity
// in state from moves to state to once the instant instant returns has come;
// false means no instant for now. The engine asks again each time the entity
// is written, and its loop sleeps until the earliest instant.
func (d *MachineSpec[E, S]) At(event string, from, to S, instant func(E) (time.Time, bool)) *MachineSpec[E, S] {
	//: a deadline transition with no deadline function can never fire.
	if instant == nil {
		d.problem(FunctionMissing, errs.String("function", "instant"), errs.String("event", event))
	}
	//: kept even when refused, so a drawing of the declaration stays whole.
	return d.add(arrow[E, S]{event: event, from: from, to: to, trigger: corestm.TriggerDeadline, instant: instant})
}

// When declares a guard transition: an entity in state from moves to state to
// as soon as guard holds. The guard sees the entity only and is asked each time
// the entity is written, never on a clock.
func (d *MachineSpec[E, S]) When(event string, from, to S, guard func(E) bool) *MachineSpec[E, S] {
	//: a guard transition with no guard can never fire.
	if guard == nil {
		d.problem(FunctionMissing, errs.String("function", "guard"), errs.String("event", event))
	}
	//: kept even when refused, so a drawing of the declaration stays whole.
	return d.add(arrow[E, S]{event: event, from: from, to: to, trigger: corestm.TriggerGuard, guard: guard})
}

// OnEnter adds a hook run when an entity enters state, before it is stored.
// Hooks of one state run in declaration order.
//
// An error cancels the transition, and so does a panic — recovered as
// [HookPanicked], with the entity's lock released on the way out. A hook may
// change the entity but not its state nor its key ([HookChangedState],
// [HookChangedKey]), and must not fire its own machine: it runs under the
// entity's lock, so the call is refused with [Reentrant] rather than waiting
// for itself.
func (d *MachineSpec[E, S]) OnEnter(state S, hook func(context.Context, *E) error) *MachineSpec[E, S] {
	//: a nil hook would panic inside every transition into state.
	if hook == nil {
		d.problem(FunctionMissing, errs.String("function", "on-enter"), errs.String("state", fmt.Sprint(state)))
		//: not recorded: there is nothing to run.
		return d
	}
	d.enter[state] = append(d.enter[state], hook)
	d.see(state)
	//: fluent, so a declaration reads as one expression.
	return d
}

// OnTransition adds a hook run after every stored transition, whoever fired
// it — the natural place to publish it. Hooks run in declaration order, once
// the entity's lock is released, so a hook may fire the machine again. An
// error or a panic is reported through Config.Report; the transition stands
// and the next hooks still run.
func (d *MachineSpec[E, S]) OnTransition(hook func(context.Context, ChangeValue[E, S]) error) *MachineSpec[E, S] {
	//: a nil hook would panic after every transition.
	if hook == nil {
		d.problem(FunctionMissing, errs.String("function", "on-transition"))
		//: not recorded: there is nothing to run.
		return d
	}
	d.after = append(d.after, hook)
	//: fluent, so a declaration reads as one expression.
	return d
}

// Problems returns the declaration's mistakes so far, in the order they were
// made, each an error carrying a code of this package and log-only fields
// naming the event, the state or the function concerned. A missing Initial is
// not among them — it is only a mistake once the declaration is complete, and
// NewStateMachine reports it as [InitialMissing].
func (d *MachineSpec[E, S]) Problems() []error {
	//: a copy: the caller may keep it while the declaration goes on.
	return slices.Clone(d.problems)
}

// States returns every state the declaration names, in the order each was
// first mentioned: the initial state, the ends of each transition, the states
// that have hooks.
func (d *MachineSpec[E, S]) States() []S {
	//: a copy, so a caller cannot reorder the declaration.
	return slices.Clone(d.order)
}

// Transitions returns the declared transitions in declaration order.
func (d *MachineSpec[E, S]) Transitions() []TransitionValue[S] {
	out := make([]TransitionValue[S], 0, len(d.arrows))
	//: one read-back value per declared arrow.
	for _, x := range d.arrows {
		out = append(out, TransitionValue[S]{Event: x.event, From: x.from, To: x.to, Trigger: x.trigger, Delay: x.delay})
	}
	//: declaration order, which is also the order automatic ones are tried.
	return out
}

// InitialState returns the state a new entity enters, and whether Initial
// was called.
func (d *MachineSpec[E, S]) InitialState() (state S, set bool) {
	//: the pair, so a zero S is never mistaken for a declared one.
	return d.initial, d.hasInitial
}

// Can reports whether [StateMachine.Fire] with event would move entity: an event
// transition by that name leaves its current state. It is a function of the
// declaration alone and needs no running machine.
func (d *MachineSpec[E, S]) Can(entity E, event string) bool {
	//: a definition refused for its accessor answers no rather than panic.
	if d.state == nil {
		//: nothing can be read from the entity.
		return false
	}
	current := *d.state(&entity)
	//: event transitions only: timers and guards are the loop's.
	return slices.ContainsFunc(d.arrows, func(x arrow[E, S]) bool {
		//: the name, the state left, and the kind all have to agree.
		return x.event == event && x.from == current && x.trigger == corestm.TriggerEvent
	})
}

// add records a transition, refusing an unusable name and a duplicate.
func (d *MachineSpec[E, S]) add(a arrow[E, S]) *MachineSpec[E, S] {
	//: an empty name cannot be fired, and the creation's name is the history's.
	if a.event == "" || a.event == corestm.CreateEvent {
		d.problem(EventInvalid, errs.String("event", a.event))
		//: not recorded: nothing could ever address it.
		return d
	}
	//: one transition per event and state, or Fire would have two answers.
	if slices.ContainsFunc(d.arrows, func(x arrow[E, S]) bool { return x.event == a.event && x.from == a.from }) {
		d.problem(TransitionDuplicate, errs.String("event", a.event), errs.String("state", fmt.Sprint(a.from)))
		//: the first declaration stands.
		return d
	}
	d.arrows = append(d.arrows, a)
	d.see(a.from, a.to)
	//: fluent, so a declaration reads as one expression.
	return d
}

// see records states in the order they are first mentioned.
func (d *MachineSpec[E, S]) see(states ...S) {
	//: first mention wins; a later one changes nothing.
	for _, st := range states {
		//: a state already listed keeps its place.
		if !slices.Contains(d.order, st) {
			d.order = append(d.order, st)
		}
	}
}

// problem records one declaration mistake with its fields.
func (d *MachineSpec[E, S]) problem(sentinel *errs.Error, fields ...errs.FieldValue) {
	d.problems = append(d.problems, errs.Wrap(sentinel, errs.WrapParams{}, fields...))
}
