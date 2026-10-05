package statemachine

import (
	"context"
	"fmt"
	"slices"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/app/statemachine"
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

// NewMachineSpec starts the declaration of a machine over entities of type E.
// state returns a pointer to the entity's state field: the engine reads the
// state through it and writes the new state through it, so the store stays
// the single source of truth.
func NewMachineSpec[E any, S comparable](state func(*E) *S) *MachineSpec[E, S] {
	d := &MachineSpec[E, S]{state: state, enter: make(map[S][]func(context.Context, *E) error)}
	//: a nil accessor is a problem to report, not a panic at package init.
	if state == nil {
		d.problem(corestm.FunctionMissing, errs.String("function", "state"))
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
		d.problem(corestm.DelayInvalid, errs.String("event", event))
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
		d.problem(corestm.FunctionMissing, errs.String("function", "instant"), errs.String("event", event))
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
		d.problem(corestm.FunctionMissing, errs.String("function", "guard"), errs.String("event", event))
	}
	//: kept even when refused, so a drawing of the declaration stays whole.
	return d.add(arrow[E, S]{event: event, from: from, to: to, trigger: corestm.TriggerGuard, guard: guard})
}

// OnEnter adds a hook run when an entity enters state, before it is stored.
// Hooks of one state run in declaration order.
//
// An error cancels the transition, and so does a panic — recovered as
// [corestm.HookPanicked], with the entity's lock released on the way out. A hook may
// change the entity but not its state nor its key ([corestm.HookChangedState],
// [corestm.HookChangedKey]), and must not fire its own machine: it runs under the
// entity's lock, so the call is refused with [corestm.Reentrant] rather than waiting
// for itself.
func (d *MachineSpec[E, S]) OnEnter(state S, hook func(context.Context, *E) error) *MachineSpec[E, S] {
	//: a nil hook would panic inside every transition into state.
	if hook == nil {
		d.problem(corestm.FunctionMissing, errs.String("function", "on-enter"), errs.String("state", fmt.Sprint(state)))
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
		d.problem(corestm.FunctionMissing, errs.String("function", "on-transition"))
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
// NewStateMachine reports it as [corestm.InitialMissing].
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
		d.problem(corestm.EventInvalid, errs.String("event", a.event))
		//: not recorded: nothing could ever address it.
		return d
	}
	//: one transition per event and state, or Fire would have two answers.
	if slices.ContainsFunc(d.arrows, func(x arrow[E, S]) bool { return x.event == a.event && x.from == a.from }) {
		d.problem(corestm.TransitionDuplicate, errs.String("event", a.event), errs.String("state", fmt.Sprint(a.from)))
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
