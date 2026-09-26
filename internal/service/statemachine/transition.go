// Package statemachine — hosts the transitions a caller asks for (Start,
// Fire) and the part every transition shares: the hooks, the write, the step
// recorded, and the lock released before the OnTransition hooks.
package statemachine

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// hookOnEnter and hookOnTransition name the two kinds of hook in a field.
const (
	hookOnEnter      string = "on-enter"
	hookOnTransition string = "on-transition"
)

// pending is a transition decided under its entity's lock and not yet
// stored.
type pending[E any, S comparable] struct {
	// entity is the entity as read, or as handed to Start.
	entity E
	// key is the entity's key.
	key string
	// actor is who fired it; empty when the loop did.
	actor string
	// arrow is the transition taken.
	arrow arrow[E, S]
	// create says it is a Start: the entity is inserted, not replaced.
	create bool
}

// Start enters a new entity into the machine: it sets the initial state, runs
// the initial state's OnEnter hooks and inserts the entity, then runs the
// OnTransition hooks. The step recorded is core/statemachine.CreateEvent.
//
// It returns the entity as stored. It refuses an entity whose key is empty
// ([KeyEmpty]) or already taken ([EntityExists]), and a call made through the
// context an OnEnter hook of this machine was given ([Reentrant]).
func (m *StateMachine[E, S]) Start(ctx context.Context, entity E) (E, error) {
	var zero E
	//: a hook of this machine holds a lock the call would wait for.
	if inside(ctx, m.locks) {
		//: refused rather than deadlocked.
		return zero, Reentrant
	}
	key := m.store.Key(entity)
	//: every entity with no key would be the same entity.
	if key == "" {
		//: nothing was stored.
		return zero, KeyEmpty
	}
	release, err := m.locks.acquire(ctx, key)
	//: the caller stopped waiting for another transition of this key.
	if err != nil {
		//: nothing was read or stored.
		return zero, err
	}
	//: released early by transition; this covers a store that panics.
	defer release()
	p := pending[E, S]{entity: entity, key: key, arrow: m.plan.creation, create: true}
	m.cfg.sign(ctx, &p)
	change, err := m.transition(ctx, p, release)
	//: the hooks, the store or the key refused the creation.
	if err != nil {
		//: nothing was stored.
		return zero, err
	}
	//: the entity as the store holds it.
	return change.Entity, nil
}

// Fire moves the entity under key along the event transition by that name
// leaving its current state, and returns it as stored.
//
// It refuses a key the store does not hold ([EntityMissing]), an event no
// transition takes from the entity's state ([TransitionRefused] — returned
// with the entity, whose state says why), and a call made through the context
// an OnEnter hook of this machine was given ([Reentrant]). On any other
// failure — a hook, the store — it returns the entity as it was read.
func (m *StateMachine[E, S]) Fire(ctx context.Context, key, event string) (E, error) {
	var zero E
	//: a hook of this machine holds a lock the call would wait for.
	if inside(ctx, m.locks) {
		//: refused rather than deadlocked.
		return zero, Reentrant
	}
	release, err := m.locks.acquire(ctx, key)
	//: the caller stopped waiting for another transition of this key.
	if err != nil {
		//: nothing was read or stored.
		return zero, err
	}
	//: released early by transition; this covers every other way out.
	defer release()
	entity, found, err := m.store.Get(ctx, key)
	//: the store could not say, or holds nothing under the key.
	if err != nil || !found {
		//: a failed read is the store's; an absent entity is the caller's.
		return zero, missingOr(err, key)
	}
	from := *m.plan.state(&entity)
	x, ok := m.plan.eventArrow(event, from)
	//: no transition by that name leaves the entity's state.
	if !ok {
		//: the entity comes back with the refusal: its state says why.
		return entity, errs.Wrap(TransitionRefused, errs.WrapParams{}, errs.String("key", key),
			errs.String("event", event), errs.String("state", fmt.Sprint(from)))
	}
	p := pending[E, S]{entity: entity, key: key, arrow: x}
	m.cfg.sign(ctx, &p)
	change, err := m.transition(ctx, p, release)
	//: a hook or the store refused it; nothing was stored.
	if err != nil {
		//: the entity as it was read.
		return entity, err
	}
	//: the entity as the store holds it.
	return change.Entity, nil
}

// missingOr is the error of a read that failed or found nothing.
func missingOr(err error, key string) error {
	//: the store failed: that is the error.
	if err != nil {
		//: named after the operation.
		return storeFailure(err, "get")
	}
	//: the store answered, and holds nothing under key.
	return errs.Wrap(EntityMissing, errs.WrapParams{}, errs.String("key", key))
}

// transition stores p, releases the entity's lock and runs the OnTransition
// hooks. The caller holds the lock and hands its idempotent release over: it
// is called before the OnTransition hooks, on every path — a panic included.
func (m *StateMachine[E, S]) transition(ctx context.Context, p pending[E, S], release func()) (ChangeValue[E, S], error) {
	change, err := m.locked(ctx, p, release)
	//: nothing was stored, so there is nothing to plan or announce.
	if err != nil {
		//: the refusal, as commit built it.
		return change, err
	}
	m.replan(p.key, true)
	m.announce(ctx, change)
	//: stored and announced.
	return change, nil
}

// locked is the part of a transition that holds the entity's lock: the
// flight, the commit and the settling, with the lock released on the way out
// whatever happens — a hook's panic is already an error, but a store's is not.
func (m *StateMachine[E, S]) locked(ctx context.Context, p pending[E, S], release func()) (ChangeValue[E, S], error) {
	defer release()
	f := m.book.fly(p.key)
	//: deferred after the release, so it runs first: the flight lands, and
	//: a write during it is read, before the lock is given back.
	defer m.settle(ctx, p.key)
	//: the hooks, the checks, the write and the step.
	return m.commit(ctx, p, f)
}

// commit performs p under the entity's lock: the new state, the OnEnter hooks,
// the checks, the write and the recorded step. A hook that fails or panics
// ends it with nothing stored.
func (m *StateMachine[E, S]) commit(ctx context.Context, p pending[E, S], f *flight) (ChangeValue[E, S], error) {
	entity := p.entity
	*m.plan.state(&entity) = p.arrow.to
	hctx := within(ctx, m.locks)
	//: every hook of the state entered, in declaration order.
	for _, hook := range m.plan.enter[p.arrow.to] {
		//: the first failure cancels the transition.
		if err := m.enter(hctx, hook, &entity, p); err != nil {
			//: nothing was stored.
			return ChangeValue[E, S]{}, err
		}
	}
	//: a hook may change the entity, not where it goes nor what it is.
	if err := m.unchanged(entity, p); err != nil {
		//: nothing was stored.
		return ChangeValue[E, S]{}, err
	}
	//: the store has the last word on existence.
	if err := m.write(ctx, entity, p); err != nil {
		//: nothing was stored.
		return ChangeValue[E, S]{}, err
	}
	at := m.cfg.clock.Now()
	m.book.step(ctx, p.key, corestm.StepValue[S]{
		Event: p.arrow.event, From: p.arrow.from, To: p.arrow.to, Trigger: p.arrow.trigger, Actor: p.actor, At: at,
	}, f)
	//: the change the OnTransition hooks receive.
	return ChangeValue[E, S]{
		Entity: entity, At: at, Key: p.key, Event: p.arrow.event, Actor: p.actor,
		From: p.arrow.from, To: p.arrow.to, Trigger: p.arrow.trigger,
	}, nil
}

// unchanged refuses an entity whose OnEnter hooks moved its state or its key.
func (m *StateMachine[E, S]) unchanged(entity E, p pending[E, S]) error {
	//: the history would say one state and the store hold another.
	if *m.plan.state(&entity) != p.arrow.to {
		//: named after the transition.
		return errs.Wrap(HookChangedState, errs.WrapParams{}, errs.String("event", p.arrow.event))
	}
	//: storing it would write another entity.
	if m.store.Key(entity) != p.key {
		//: named after the transition.
		return errs.Wrap(HookChangedKey, errs.WrapParams{}, errs.String("event", p.arrow.event))
	}
	//: the hooks stayed within bounds.
	return nil
}

// write inserts a created entity, or replaces the one the transition read.
func (m *StateMachine[E, S]) write(ctx context.Context, entity E, p pending[E, S]) error {
	operation, write := "replace", m.store.Replace
	//: a creation expects the key free.
	if p.create {
		operation, write = "insert", m.store.Insert
	}
	stored, err := write(ctx, entity)
	//: the store failed.
	if err != nil {
		//: named after the operation.
		return storeFailure(err, operation)
	}
	//: the store answered: the key is taken, or the entity is gone.
	if !stored {
		//: which one depends on what was asked.
		return refusedWrite(p.create, p.key)
	}
	//: stored.
	return nil
}

// refusedWrite is the error of a write the store declined.
func refusedWrite(create bool, key string) error {
	//: an insert over a taken key.
	if create {
		//: the creation collided.
		return errs.Wrap(EntityExists, errs.WrapParams{}, errs.String("key", key))
	}
	//: a replace of an entity deleted while the transition ran: never a
	//: resurrection.
	return errs.Wrap(EntityMissing, errs.WrapParams{}, errs.String("key", key))
}

// settle ends the entity's flight; when the entity was written meanwhile, it
// reads it again so the record describes what the store holds. The caller
// still holds the entity's lock.
func (m *StateMachine[E, S]) settle(ctx context.Context, key string) {
	//: nothing arrived during the flight.
	if !m.book.land(key) {
		//: the record already says what the transition stored.
		return
	}
	//: reported: the transition itself succeeded or failed on its own terms.
	if err := m.refresh(ctx, key); err != nil {
		m.cfg.reportErr(ctx, err)
	}
}

// enter runs one OnEnter hook with its panic recovered.
func (m *StateMachine[E, S]) enter(ctx context.Context, hook func(context.Context, *E) error, entity *E, p pending[E, S]) (err error) {
	defer func() {
		value := recover()
		//: the ordinary path.
		if value == nil {
			//: err stays what the hook returned.
			return
		}
		//: the transition fails, and the deferred release frees the entity.
		err = hookPanic(hookOnEnter, p.arrow.event, value)
	}()
	//: a returned error cancels the transition, joined beside the verdict.
	if herr := hook(ctx, entity); herr != nil {
		//: both errors.Is and errs.HasCode answer.
		return errors.Join(errs.Wrap(HookFailed, errs.WrapParams{}, errs.String("hook", hookOnEnter),
			errs.String("event", p.arrow.event), errs.String("state", fmt.Sprint(p.arrow.to))), herr)
	}
	//: the hook accepted the transition.
	return nil
}

// announce runs the OnTransition hooks after a stored transition. A failure
// is reported, and the next hook still runs.
func (m *StateMachine[E, S]) announce(ctx context.Context, change ChangeValue[E, S]) {
	//: every hook, in declaration order, whatever the previous one did.
	for _, hook := range m.plan.after {
		//: the transition stands; the failure is the hook's.
		if err := m.notify(ctx, hook, change); err != nil {
			m.cfg.reportErr(ctx, err)
		}
	}
}

// notify runs one OnTransition hook with its panic recovered.
func (m *StateMachine[E, S]) notify(ctx context.Context, hook func(context.Context, ChangeValue[E, S]) error, change ChangeValue[E, S]) (err error) {
	defer func() {
		value := recover()
		//: the ordinary path.
		if value == nil {
			//: err stays what the hook returned.
			return
		}
		err = hookPanic(hookOnTransition, change.Event, value)
	}()
	//: a returned error is reported beside the verdict.
	if herr := hook(ctx, change); herr != nil {
		//: both errors.Is and errs.HasCode answer.
		return errors.Join(errs.Wrap(HookFailed, errs.WrapParams{}, errs.String("hook", hookOnTransition),
			errs.String("event", change.Event), errs.String("key", change.Key)), herr)
	}
	//: the hook ran.
	return nil
}

// hookPanic builds the error of a hook that panicked, with the stack of the
// goroutine that panicked — captured here, inside the deferred recover, while
// its frames are still on the stack.
func hookPanic(kind, event string, value any) error {
	//: the value travels as a field, never as the wrap origin, so a panic
	//: carrying an SDK error cannot pass for another code.
	return errs.Wrap(HookPanicked, errs.WrapParams{}, errs.String("hook", kind), errs.String("event", event),
		errs.String("panic", fmt.Sprint(value)), errs.String("stack", string(debug.Stack())))
}
