// Package statemachine — hosts the book: what a machine keeps beside the
// store — one record per entity, the census per state, the transitions in
// flight — and its writes through the Journal.
package statemachine

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// flight marks an entity whose transition holds its lock. A notification that
// arrives during the flight is recorded on it instead of waiting for the lock
// — the notification is very often the transition's own write, and waiting
// would be waiting for itself.
type flight struct {
	// deleted says the entity was deleted during the flight: its record must
	// not come back when the transition records its step.
	deleted bool
	// touched says the entity was written during the flight: the transition
	// reads it again once it has stored, so the record ends up describing
	// what the store holds.
	touched bool
}

// record is one entity's bookkeeping.
type record[S comparable] struct {
	// entered is when the entity entered state.
	entered time.Time
	// state is the state the entity is in.
	state S
	// history is the latest transitions, oldest first.
	history []corestm.StepValue[S]
}

// book is a machine's bookkeeping. mu guards every field and serialises the
// journal writes, so two saves of one key reach the journal in order.
type book[S comparable] struct {
	// journal persists the records; nil keeps them in memory only.
	journal corestm.Journal[S]
	// records holds one record per entity the machine knows.
	records map[string]*record[S]
	// census counts entities per state; every declared state is present.
	census map[S]int
	// declared says which states the definition names, so a census entry
	// for an undeclared state goes when its count falls to zero.
	declared map[S]bool
	// flights marks the entities whose transition holds their lock.
	flights map[string]*flight
	// report hands on a journal write that failed.
	report func(context.Context, error)
	// limit is how many steps a record keeps.
	limit int
	mu    sync.Mutex
}

// newBook returns an empty book counting every declared state at zero.
func newBook[S comparable](journal corestm.Journal[S], census map[S]int, limit int, report func(context.Context, error)) *book[S] {
	declared := make(map[S]bool, len(census))
	//: remember which states are the definition's.
	for state := range census {
		declared[state] = true
	}
	//: ready for the opening reconciliation.
	return &book[S]{
		journal: journal, records: make(map[string]*record[S]), census: census, declared: declared,
		flights: make(map[string]*flight), report: report, limit: limit,
	}
}

// adjust moves the census count of state by delta. The caller holds mu.
func (b *book[S]) adjust(state S, delta int) {
	b.census[state] += delta
	//: an undeclared state leaves the census with its last entity.
	if b.census[state] <= 0 && !b.declared[state] {
		delete(b.census, state)
	}
}

// adopt takes rec as its entity's record, as the opening reconciled it. A
// history longer than the configured length keeps its latest steps.
func (b *book[S]) adopt(rec corestm.RecordValue[S]) {
	b.mu.Lock()
	defer b.mu.Unlock()
	history := rec.History
	//: a journal written under a longer limit keeps only the latest steps.
	if over := len(history) - b.limit; over > 0 {
		history = history[over:]
	}
	b.records[rec.Key] = &record[S]{state: rec.State, entered: rec.Entered, history: slices.Clone(history)}
	b.adjust(rec.State, 1)
}

// fly marks key as held by a transition. The caller holds the key's lock.
func (b *book[S]) fly(key string) *flight {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := &flight{}
	b.flights[key] = f
	//: the transition keeps it to learn what happened while it held the lock.
	return f
}

// land ends key's flight and says whether the entity was written meanwhile.
func (b *book[S]) land(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := b.flights[key]
	delete(b.flights, key)
	//: no flight means nothing was recorded against it.
	if f == nil {
		//: untouched.
		return false
	}
	//: a deleted entity is not read again: there is nothing to read.
	return f.touched && !f.deleted
}

// touch records a write of key against its flight, and reports whether there
// was one — in which case the transition in flight reads the entity again
// once it has stored, and the caller must not wait for the lock.
func (b *book[S]) touch(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := b.flights[key]
	//: no transition holds the entity.
	if f == nil {
		//: the caller takes the lock and reads.
		return false
	}
	f.touched = true
	//: the flight will read it.
	return true
}

// step records a stored transition of key: its state, when it entered it and
// the step in its history. Nothing is recorded when the entity was deleted
// during the flight.
func (b *book[S]) step(ctx context.Context, key string, s corestm.StepValue[S], f *flight) {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: the entity is gone; its record must not come back.
	if f != nil && f.deleted {
		//: nothing to record.
		return
	}
	rec := b.records[key]
	//: a creation, or an entity the book had not met.
	if rec == nil {
		rec = &record[S]{state: s.To}
		b.records[key] = rec
		b.adjust(s.To, 1)
	} else {
		b.adjust(rec.state, -1)
		b.adjust(s.To, 1)
	}
	rec.state, rec.entered = s.To, s.At
	rec.history = append(rec.history, s)
	//: keep only the latest steps; the oldest go first.
	if over := len(rec.history) - b.limit; over > 0 {
		rec.history = slices.Delete(rec.history, 0, over)
	}
	b.save(ctx, key, rec)
}

// reconcile brings key's record in line with state, read from the store at
// now. A state the record does not hold was changed behind the machine's back:
// the record takes it with Entered set to now, and no step — the machine knows
// the state, not a transition. It reports whether the state changed.
func (b *book[S]) reconcile(ctx context.Context, key string, state S, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec := b.records[key]
	//: an entity the machine meets for the first time.
	if rec == nil {
		rec = &record[S]{state: state, entered: now}
		b.records[key] = rec
		b.adjust(state, 1)
		b.save(ctx, key, rec)
		//: new to the book.
		return true
	}
	//: the record already says so.
	if rec.state == state {
		//: nothing moved.
		return false
	}
	b.adjust(rec.state, -1)
	b.adjust(state, 1)
	rec.state, rec.entered = state, now
	b.save(ctx, key, rec)
	//: the state moved without a transition.
	return true
}

// forget drops key's record — the entity was deleted — and tells a flight
// holding it, so the transition's step does not bring the record back.
func (b *book[S]) forget(ctx context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: a transition in flight must not record the entity again.
	if f := b.flights[key]; f != nil {
		f.deleted = true
	}
	rec := b.records[key]
	//: an entity the book never met, or already forgot.
	if rec == nil {
		//: nothing to drop, nothing to tell the journal.
		return nil
	}
	b.adjust(rec.state, -1)
	delete(b.records, key)
	//: the journal forgets it too, when there is one.
	if b.journal == nil {
		//: memory only.
		return nil
	}
	//: the caller receives the failure; the record is gone either way.
	return journalFailure(b.journal.Delete(ctx, key), "delete")
}

// stateOf returns the state key's record holds.
func (b *book[S]) stateOf(key string) (state S, known bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec := b.records[key]
	//: not known, or forgotten.
	if rec == nil {
		//: the zero state and false.
		return state, false
	}
	//: the state as last recorded.
	return rec.state, true
}

// entered returns when key entered its state, as the book knows it.
func (b *book[S]) entered(key string) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec := b.records[key]
	//: not known: the caller reconciles first.
	if rec == nil {
		//: unknown.
		return time.Time{}, false
	}
	//: the instant the delay timers count from.
	return rec.entered, true
}

// save writes rec to the journal. The caller holds mu, which is what orders
// two saves of one key. A failure is reported: the store is the source of
// truth, and a lost record only costs a timer its origin after a restart.
func (b *book[S]) save(ctx context.Context, key string, rec *record[S]) {
	//: memory only.
	if b.journal == nil {
		//: nothing to write.
		return
	}
	//: reported, never returned: the transition it follows stands.
	if err := b.journal.Save(ctx, rec.value(key)); err != nil {
		b.report(ctx, journalFailure(err, "save"))
	}
}

// value renders r as the journal's value, its history copied.
func (r *record[S]) value(key string) corestm.RecordValue[S] {
	//: a copy, so the caller can keep it while the book goes on.
	return corestm.RecordValue[S]{Key: key, State: r.state, Entered: r.entered, History: slices.Clone(r.history)}
}

// censusCopy returns the count of entities per state.
func (b *book[S]) censusCopy() map[S]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: a copy the caller owns; every declared state is present.
	return maps.Clone(b.census)
}

// lookup returns key's record.
func (b *book[S]) lookup(key string) (corestm.RecordValue[S], bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec := b.records[key]
	//: not known.
	if rec == nil {
		//: the zero record and false.
		return corestm.RecordValue[S]{}, false
	}
	//: a copy.
	return rec.value(key), true
}

// all returns every record, the most recently entered state first and, for
// equal instants, by key.
func (b *book[S]) all() []corestm.RecordValue[S] {
	b.mu.Lock()
	out := make([]corestm.RecordValue[S], 0, len(b.records))
	//: copies, taken under the lock.
	for key, rec := range b.records {
		out = append(out, rec.value(key))
	}
	b.mu.Unlock()
	slices.SortFunc(out, func(x, y corestm.RecordValue[S]) int {
		//: newest first, then a stable order for equal instants.
		return cmp.Or(y.Entered.Compare(x.Entered), cmp.Compare(x.Key, y.Key))
	})
	//: sorted outside the lock.
	return out
}

// journalFailure wraps a journal error with the operation that failed; nil
// stays nil.
func journalFailure(err error, operation string) error {
	//: nothing failed.
	if err == nil {
		//: nil stays nil.
		return nil
	}
	//: origin wins when the journal's error is already an SDK error.
	return errs.Wrap(err, errs.WrapParams{
		Code: CodeJournalFailed, Reason: "JOURNAL_FAILED", Public: JournalFailed.Public(), Private: JournalFailed.Private(),
	}, errs.String("operation", operation))
}
