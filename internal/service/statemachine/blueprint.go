// Package statemachine — hosts the blueprint: a definition frozen for one
// machine, indexed for the lookups a transition makes, and the evaluation of
// what is due for an entity.
package statemachine

import (
	"context"
	"fmt"
	"runtime/debug"
	"slices"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// eventKey addresses an event transition: its name and the state it leaves.
type eventKey[S comparable] struct {
	event string
	from  S
}

// blueprint is a MachineSpec frozen for one machine. Every slice and map is a
// copy, so a declaration that goes on after NewStateMachine reaches no machine.
type blueprint[E any, S comparable] struct {
	// state is the accessor of the entity's state field.
	state func(*E) *S
	// enter holds the OnEnter hooks per state.
	enter map[S][]func(context.Context, *E) error
	// events indexes event transitions by name and state left.
	events map[eventKey[S]]int
	// auto lists, per state, the automatic transitions leaving it, in
	// declaration order — the order the first-declared rule reads.
	auto map[S][]int
	// content says, per state, whether a transition leaving it depends on the
	// entity's content (At or When), so a write can move what is due.
	content map[S]bool
	// arrows is every transition, in declaration order.
	arrows []arrow[E, S]
	// order lists every declared state.
	order []S
	// after holds the OnTransition hooks.
	after []func(context.Context, ChangeValue[E, S]) error
	// creation is the arrow a Start takes: CreateEvent into the initial state.
	creation arrow[E, S]
}

// freeze copies d and indexes the copy.
func freeze[E any, S comparable](d *MachineSpec[E, S]) *blueprint[E, S] {
	b := &blueprint[E, S]{
		state: d.state, arrows: slices.Clone(d.arrows), order: slices.Clone(d.order), after: slices.Clone(d.after),
		enter:  make(map[S][]func(context.Context, *E) error, len(d.enter)),
		events: make(map[eventKey[S]]int), auto: make(map[S][]int), content: make(map[S]bool),
		creation: arrow[E, S]{event: corestm.CreateEvent, to: d.initial, trigger: corestm.TriggerStart},
	}
	//: the hook slices are copied too, so an append after NewStateMachine is inert.
	for state, hooks := range d.enter {
		b.enter[state] = slices.Clone(hooks)
	}
	//: one pass builds both indexes.
	for i, x := range b.arrows {
		//: event transitions are addressed by name; the others are the loop's.
		if x.trigger == corestm.TriggerEvent {
			b.events[eventKey[S]{event: x.event, from: x.from}] = i
			//: nothing more to index for an event.
			continue
		}
		b.auto[x.from] = append(b.auto[x.from], i)
		//: a deadline or a guard reads the entity, so a write can move it.
		if x.trigger != corestm.TriggerDelay {
			b.content[x.from] = true
		}
	}
	//: frozen.
	return b
}

// automatic reports whether a transition the loop fires leaves state.
func (b *blueprint[E, S]) automatic(state S) bool {
	//: an entity in any other state is never on the agenda.
	return len(b.auto[state]) > 0
}

// declared returns the declared states, for a census that lists every one.
func (b *blueprint[E, S]) declared() map[S]int {
	counts := make(map[S]int, len(b.order))
	//: every declared state is present, at zero until counted.
	for _, st := range b.order {
		counts[st] = 0
	}
	//: a fresh map the caller owns.
	return counts
}

// eventArrow returns the event transition by that name leaving from.
func (b *blueprint[E, S]) eventArrow(event string, from S) (arrow[E, S], bool) {
	i, found := b.events[eventKey[S]{event: event, from: from}]
	//: no such transition: the caller refuses the event.
	if !found {
		//: the zero arrow is never fired.
		return arrow[E, S]{}, false
	}
	//: a copy of the declared arrow.
	return b.arrows[i], true
}

// dueAt says when x is due for e, which entered its state at entered: an
// instant, possibly past; false when it is not due at all for now. A guard
// that holds is due at the zero time, before any clock.
func (x *arrow[E, S]) dueAt(e E, entered time.Time) (time.Time, bool) {
	//: one rule per kind of automatic transition.
	switch x.trigger {
	//: the guard decides, and holding means now.
	case corestm.TriggerGuard:
		//: the zero instant precedes every reading of every clock.
		return time.Time{}, x.guard(e)
	//: the entity says when, if it says anything.
	case corestm.TriggerDeadline:
		//: the caller's own instant.
		return x.instant(e)
	//: a duration counted from entering the state.
	case corestm.TriggerDelay:
		//: fixed for as long as the entity stays in the state.
		return entered.Add(x.delay), true
	//: events and creations — and anything outside the set — are never due
	//: by themselves.
	default:
		//: not the loop's to fire.
		return time.Time{}, false
	}
}

// verdict is what the evaluation of an entity decided: a transition to fire
// now, or the instant the next one falls due (zero when none will by itself).
type verdict[E any, S comparable] struct {
	fire *arrow[E, S]
	next time.Time
}

// evaluate asks every automatic transition leaving state whether it is due
// for e at now. The FIRST declared transition that is due is the one to fire;
// otherwise the verdict holds the earliest instant one falls due. A guard or
// an instant function that panics is recovered as [FunctionPanicked].
func (b *blueprint[E, S]) evaluate(e E, state S, entered, now time.Time) (due verdict[E, S], err error) {
	asking := ""
	defer func() {
		value := recover()
		//: the ordinary path.
		if value == nil {
			//: the verdict stands as computed.
			return
		}
		due = verdict[E, S]{}
		//: the value travels as a field, never as the wrap origin.
		err = errs.Wrap(FunctionPanicked, errs.WrapParams{}, errs.String("event", asking),
			errs.String("panic", fmt.Sprint(value)), errs.String("stack", string(debug.Stack())))
	}()
	//: declaration order: the first declared among those due fires.
	for _, i := range b.auto[state] {
		x := &b.arrows[i]
		asking = x.event
		at, ok := x.dueAt(e, entered)
		//: this transition has no instant for now.
		if !ok {
			//: the next one may.
			continue
		}
		//: due now or already past.
		if !at.After(now) {
			//: this one fires.
			return verdict[E, S]{fire: x}, nil
		}
		//: keep the earliest instant ahead.
		if due.next.IsZero() || at.Before(due.next) {
			due.next = at
		}
	}
	//: nothing due now; due.next says when, zero for never by itself.
	return due, nil
}
