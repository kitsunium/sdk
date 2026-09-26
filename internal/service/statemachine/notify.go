// Package statemachine — hosts what a store's owner tells a machine about
// writes it did not make, and how the machine brings its records and its
// agenda in line with them.
package statemachine

import "context"

// Changed tells the machine the entity under key was written by something
// other than the machine — call it after the write is visible to Store.Get.
// The machine reads the entity: a state that changed behind its back enters
// its record as of now, and when a timer or a guard leaves the entity's state
// the loop is woken to ask again what is due — a write can move a deadline or
// make a guard hold. An entity the store no longer holds is forgotten, as by
// [StateMachine.Deleted].
//
// Calling it for the machine's own writes is harmless. When a transition of
// the entity is in flight — which is where a store that notifies on every
// write calls it from — it returns at once and the transition reads the
// entity again before it lets go of it. Call it OUTSIDE any lock of the
// store: it may wait for the entity's lock while the loop, holding it, reads
// the store.
func (m *StateMachine[E, S]) Changed(ctx context.Context, key string) error {
	//: a transition holds the entity: it will read the write itself.
	if m.book.touch(key) {
		//: waiting for the lock would be waiting for the caller's own write.
		return nil
	}
	release, err := m.locks.acquire(ctx, key)
	//: the caller stopped waiting; the loop reads the entity later.
	if err != nil {
		m.agenda.markDirty(key)
		//: the context's end, as WaitAbandoned.
		return err
	}
	defer release()
	//: read under the lock, so no transition's record overtakes the read.
	return m.refresh(ctx, key)
}

// Deleted tells the machine the entity under key was deleted: its record
// leaves the census, the journal and the agenda. A transition of the entity in
// flight will not record it again. It returns the journal's failure, if any;
// the record is dropped either way.
func (m *StateMachine[E, S]) Deleted(ctx context.Context, key string) error {
	m.agenda.forget(key)
	//: no lock: a hook deleting its own entity runs under it.
	return m.book.forget(ctx, key)
}

// refresh reads the entity under key and brings its record and its place on
// the agenda in line. The caller holds the entity's lock.
func (m *StateMachine[E, S]) refresh(ctx context.Context, key string) error {
	entity, found, err := m.store.Get(ctx, key)
	//: the store could not say.
	if err != nil {
		//: named after the operation.
		return storeFailure(err, "get")
	}
	//: gone: forget it the way Deleted would.
	if !found {
		//: the journal's failure, if any.
		return m.Deleted(ctx, key)
	}
	changed := m.book.reconcile(ctx, key, *m.plan.state(&entity), m.cfg.clock.Now())
	m.replan(key, changed)
	//: in line.
	return nil
}

// replan puts key on the agenda for the loop to evaluate, or takes it off
// when no timer and no guard leaves its state. changed says the entity entered
// a state — a transition, or a change behind the machine's back — which
// clears the failures of the state it left and always needs a new look;
// otherwise only a state whose deadlines or guards read the entity does.
func (m *StateMachine[E, S]) replan(key string, changed bool) {
	state, known := m.book.stateOf(key)
	//: an entity the book no longer holds, or in a state nothing moves out
	//: of by itself, has no place on the agenda.
	if !known || !m.plan.automatic(state) {
		m.agenda.unschedule(key)
		//: only a caller's event can move it now.
		return
	}
	//: what failed was a transition out of the state left.
	if changed {
		m.agenda.restart(key)
	}
	//: a new state, or content a deadline or a guard reads: ask again.
	if changed || m.plan.content[state] {
		m.agenda.markDirty(key)
	}
}
