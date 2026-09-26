// Package statemachine — hosts the book: what a machine keeps beside the
// store — one record per entity, the census per state, the entities held by a
// read or a transition — and its writes through the Journal, one key at a
// time.
package statemachine

import (
	"cmp"
	"context"
	"hash/maphash"
	"maps"
	"slices"
	"sync"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// journalGates is how many gates order the journal's writes. A key always
// takes the gate its hash names, so two writes of one key are ordered; two
// keys share a gate one time in journalGates, and then only wait for each
// other's journal call.
const journalGates uint64 = 256

// What holds an entity during a flight.
const (
	// readFlight is a read the machine records: it takes deletions only — a
	// writer waits for the lock and reads after it.
	readFlight flightKind = iota + 1
	// transitionFlight is a transition: it takes writes too, since the write
	// is very often the transition's own.
	transitionFlight
)

// flightKind says what holds an entity during a flight.
type flightKind uint8

// flight marks an entity whose lock is held by something that read it or is
// moving it. A deletion that arrives meanwhile is recorded on it instead of
// waiting for the lock, so nothing the holder records afterwards brings the
// entity back; a transition's flight takes writes too — the notification is
// very often the transition's own write, and waiting for the lock would be
// waiting for itself.
type flight struct {
	// deleted says the entity was deleted during the flight: its record must
	// not come back when the holder records what it read or stored.
	deleted bool
	// touched says the entity was written during the flight: the transition
	// reads it again once it has stored, so the record ends up describing
	// what the store holds.
	touched bool
	// kind says what holds the entity, and so whether writes land here.
	kind flightKind
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

// book is a machine's bookkeeping. mu guards every field and is never held
// across a call to the caller's code: a journal write is made under the key's
// gate instead, taken BEFORE mu, so two writes of one key reach the journal in
// the order the book made them while writes of different keys do not wait for
// each other.
type book[S comparable] struct {
	// journal persists the records; nil keeps them in memory only.
	journal corestm.Journal[S]
	// seed hashes a key to its gate.
	seed maphash.Seed
	// records holds one record per entity the machine knows.
	records map[string]*record[S]
	// census counts entities per state; every declared state is present.
	census map[S]int
	// declared says which states the definition names, so a census entry
	// for an undeclared state goes when its count falls to zero.
	declared map[S]bool
	// flights marks the entities whose lock is held by a read or a
	// transition.
	flights map[string]*flight
	// limit is how many steps a record keeps.
	limit int
	mu    sync.Mutex
	// gates order the journal writes of one key; see journalGates.
	gates [journalGates]sync.Mutex
}

// newBook returns an empty book counting every declared state at zero.
func newBook[S comparable](journal corestm.Journal[S], census map[S]int, limit int) *book[S] {
	declared := make(map[S]bool, len(census))
	//: remember which states are the definition's.
	for state := range census {
		declared[state] = true
	}
	//: ready for the opening reconciliation.
	return &book[S]{
		journal: journal, seed: maphash.MakeSeed(), records: make(map[string]*record[S]), census: census,
		declared: declared, flights: make(map[string]*flight), limit: limit,
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

// fly marks key as held by a transition, which takes the writes and deletes
// that arrive meanwhile. The caller holds the key's lock.
func (b *book[S]) fly(key string) *flight {
	//: a flight that catches writes.
	return b.open(key, transitionFlight)
}

// read marks key as held by a read the caller will record: a deletion that
// arrives meanwhile is caught, a write waits for the lock. The caller holds
// the key's lock.
func (b *book[S]) read(key string) *flight {
	//: a flight that catches deletions only.
	return b.open(key, readFlight)
}

// open marks key as held by a flight of kind.
func (b *book[S]) open(key string, kind flightKind) *flight {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := &flight{kind: kind}
	b.flights[key] = f
	//: the holder keeps it to learn what happened while it held the lock.
	return f
}

// land ends f, key's flight, and says what happened during it: whether the
// entity was written — and not deleted, since a deleted entity has nothing to
// read again — and whether it was deleted. A flight already landed, or
// replaced by a later holder's, says nothing and leaves the later one alone.
func (b *book[S]) land(key string, f *flight) (touched, deleted bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: not this flight any more: landed already.
	if b.flights[key] != f {
		//: nothing recorded against it now.
		return false, false
	}
	delete(b.flights, key)
	//: a deleted entity is not read again: there is nothing to read.
	return f.touched && !f.deleted, f.deleted
}

// touch records a write of key against its transition's flight, and reports
// whether there was one — in which case the transition reads the entity again
// once it has stored, and the caller must not wait for the lock.
func (b *book[S]) touch(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := b.flights[key]
	//: no transition holds the entity; a read's flight takes no writes.
	if f == nil || f.kind != transitionFlight {
		//: the caller takes the lock and reads.
		return false
	}
	f.touched = true
	//: the flight will read it.
	return true
}

// step records a stored transition of key: its state, when it entered it and
// the step in its history, and returns the journal's failure to write it —
// for the caller to report once it holds no lock. Nothing is recorded when the
// entity was deleted during the flight.
func (b *book[S]) step(ctx context.Context, key string, s corestm.StepValue[S], f *flight) error {
	gate := b.gate(key)
	gate.Lock()
	defer gate.Unlock()
	rec, recorded := b.stepped(key, s, f)
	//: the entity is gone: nothing recorded, nothing to write.
	if !recorded {
		//: the deletion wins.
		return nil
	}
	//: the journal's verdict, for the caller.
	return b.save(ctx, rec)
}

// stepped applies step s to key's record under mu and returns a copy to
// write, or false when the entity was deleted during the flight.
func (b *book[S]) stepped(key string, s corestm.StepValue[S], f *flight) (corestm.RecordValue[S], bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: the entity is gone; its record must not come back.
	if f != nil && f.deleted {
		//: nothing to record.
		return corestm.RecordValue[S]{}, false
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
	//: the copy the journal receives.
	return rec.value(key), true
}

// reconcile brings key's record in line with state, read from the store at
// now under f, the reader's flight. A state the record does not hold was
// changed behind the machine's back: the record takes it with Entered set to
// now, and no step — the machine knows the state, not a transition. An entity
// deleted since the read is left forgotten. It reports whether the state
// changed, and the journal's failure to write it, for the caller to report.
func (b *book[S]) reconcile(ctx context.Context, key string, state S, now time.Time, f *flight) (bool, error) {
	gate := b.gate(key)
	gate.Lock()
	defer gate.Unlock()
	rec, changed := b.moved(key, state, now, f)
	//: nothing moved, or the entity is gone: nothing to write.
	if !changed {
		//: in line already.
		return false, nil
	}
	//: the state moved without a transition, written down.
	return true, b.save(ctx, rec)
}

// moved applies a state read from the store to key's record under mu, and
// returns a copy to write when the record changed.
func (b *book[S]) moved(key string, state S, now time.Time, f *flight) (corestm.RecordValue[S], bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: deleted after the read: what was read must not bring it back.
	if f != nil && f.deleted {
		//: forgotten stays forgotten.
		return corestm.RecordValue[S]{}, false
	}
	rec := b.records[key]
	//: an entity the machine meets for the first time.
	if rec == nil {
		rec = &record[S]{state: state, entered: now}
		b.records[key] = rec
		b.adjust(state, 1)
		//: new to the book.
		return rec.value(key), true
	}
	//: the record already says so.
	if rec.state == state {
		//: nothing moved.
		return corestm.RecordValue[S]{}, false
	}
	b.adjust(rec.state, -1)
	b.adjust(state, 1)
	rec.state, rec.entered = state, now
	//: moved.
	return rec.value(key), true
}

// forget drops key's record — the entity was deleted — and tells a flight
// holding it, so what its holder records next does not bring the record back.
func (b *book[S]) forget(ctx context.Context, key string) error {
	gate := b.gate(key)
	gate.Lock()
	defer gate.Unlock()
	known := b.dropped(key)
	//: an entity the book never met, or already forgot, or memory only.
	if !known || b.journal == nil {
		//: nothing to tell the journal.
		return nil
	}
	//: the caller receives the failure; the record is gone either way.
	return journalFailure(b.journal.Delete(ctx, key), "delete")
}

// dropped drops key's record under mu, marking its flight, and reports
// whether there was a record.
func (b *book[S]) dropped(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: a holder of the entity must not record it again.
	if f := b.flights[key]; f != nil {
		f.deleted = true
	}
	rec := b.records[key]
	//: nothing to drop.
	if rec == nil {
		//: unknown.
		return false
	}
	b.adjust(rec.state, -1)
	delete(b.records, key)
	//: dropped.
	return true
}

// gate returns the mutex that orders the journal writes of key: taken before
// mu, held across the journal call, never held while waiting for anything
// else — so two gates are never held at once.
func (b *book[S]) gate(key string) *sync.Mutex {
	//: a key always lands on the same gate.
	return &b.gates[maphash.String(b.seed, key)%journalGates]
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

// save writes rec to the journal. The caller holds the key's gate, which is
// what orders two writes of one key, and NOT mu: the journal is the caller's
// code. A failure is returned for the caller to REPORT once it holds no lock
// — never to fail what it follows: the store is the source of truth, and a
// lost record only costs a timer its origin after a restart.
func (b *book[S]) save(ctx context.Context, rec corestm.RecordValue[S]) error {
	//: memory only.
	if b.journal == nil {
		//: nothing to write.
		return nil
	}
	//: nil when written.
	return journalFailure(b.journal.Save(ctx, rec), "save")
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
