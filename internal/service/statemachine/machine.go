// Package statemachine is the state-machine engine over stored entities (ADR
// 0120): a [MachineSpec] declares the states an entity goes through and the
// transitions between them — events a caller fires, timers after a duration in
// a state, deadlines the entity carries, guards on the entity — with hooks on
// the way; a [StateMachine] runs it over the caller's store, keeps a record of each
// entity beside it, and fires the timers and guards from its own loop, which
// sleeps until the next transition due and wakes on a write. The ports it is
// given — the Store, the Journal — are internal/core/statemachine's.
//
// # One entity, one transition at a time
//
// A transition takes its entity's lock, reads it again, runs the OnEnter hooks
// of the state entered, writes it — an insert for a creation, a replace
// otherwise, and a replace never brings back an entity deleted meanwhile —
// records the step, releases the lock, and only then runs the OnTransition
// hooks. A hook that panics fails what it was part of and never leaves an
// entity locked. Transitions of different entities run concurrently.
//
// # The agenda
//
// Each entity in a state that a timer or a guard leaves has ONE entry on a
// heap: the instant its earliest automatic transition falls due. The loop
// pops what is due, evaluates only those entities and the ones written since
// it last looked, and sleeps until the heap's top — so finding the next
// transition due costs O(log N), where re-reading the whole store on every
// wake costs O(N). BENCH.md measures both.
package statemachine

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync/atomic"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// StateMachine runs a [MachineSpec] over the entities of a [corestm.Store]: it
// creates them in their initial state, moves them along event transitions when
// a caller fires one, and — while [StateMachine.Run] goes — along the timers and
// guards that are due. It is safe for concurrent use.
//
// Transitions of one entity run one at a time; transitions of different
// entities run concurrently. Beside the store the machine keeps one record per
// entity — its state, since when, its latest transitions — in memory and in
// the configured journal, plus the census of entities per state.
//
// The store may be written by others. Tell the machine through
// [StateMachine.Changed] and [StateMachine.Deleted]: a write can move a deadline or
// make a guard hold, and a state changed behind the machine's back re-enters
// its state at the moment the machine learns of it.
type StateMachine[E any, S comparable] struct {
	store  corestm.Store[E]
	plan   *blueprint[E, S]
	book   *book[S]
	agenda *agenda
	locks  *keyLocks
	cfg    settings[E, S]
	// running is held by a Run or a Step while it goes.
	running atomic.Bool
}

// NewStateMachine checks def and cfg, then opens a machine over cfg.Store: it
// reads the journal, reads every entity once, reconciles the two — an entity
// the journal does not know, or whose state differs, enters its state now; a
// record whose entity is gone is dropped — and schedules what is due. It is
// the only moment the machine reads the whole store.
//
// A definition with problems is refused with every problem joined, a missing
// Initial as [InitialMissing]; a nil or storeless cfg with [StoreMissing]; a
// journal that cannot be loaded, or a store that cannot be read, with
// [JournalFailed] or [StoreFailed]. The machine keeps its own copy of def,
// and reads cfg once.
func NewStateMachine[E any, S comparable](ctx context.Context, def *MachineSpec[E, S], cfg *Config[E, S]) (*StateMachine[E, S], error) {
	//: a nil definition has nothing to run.
	if def == nil {
		//: named like every other missing function.
		return nil, errs.Wrap(FunctionMissing, errs.WrapParams{}, errs.String("function", "definition"))
	}
	//: a nil configuration is the zero one, which names no store.
	if cfg == nil {
		cfg = &Config[E, S]{}
	}
	//: every problem at once, so one run of the program lists them all.
	if problems := checked(def, cfg); len(problems) > 0 {
		//: errs.HasCode answers for each joined problem.
		return nil, errors.Join(problems...)
	}
	plan := freeze(def)
	m := &StateMachine[E, S]{store: cfg.Store, plan: plan, cfg: resolve(cfg), agenda: newAgenda(), locks: newKeyLocks()}
	m.book = newBook(cfg.Journal, plan.declared(), m.cfg.history)
	//: the one full read of the store.
	if err := m.open(ctx); err != nil {
		//: nothing half-open is handed out.
		return nil, err
	}
	//: ready for Start, Fire and Run.
	return m, nil
}

// checked returns the problems of def and cfg together: the declaration's
// own, a missing Initial and a missing store.
func checked[E any, S comparable](def *MachineSpec[E, S], cfg *Config[E, S]) []error {
	problems := def.Problems()
	//: Initial is only a problem once the declaration is complete.
	if _, set := def.InitialState(); !set {
		problems = append(problems, InitialMissing)
	}
	//: a store is required.
	if cfg.Store == nil {
		problems = append(problems, StoreMissing)
	}
	//: every problem found.
	return problems
}

// open reconciles the journal with the store and schedules every entity in a
// state an automatic transition leaves.
func (m *StateMachine[E, S]) open(ctx context.Context) error {
	loaded, err := m.loadJournal(ctx)
	//: a journal that cannot be read would reset every timer silently.
	if err != nil {
		//: refuse to open rather than lose what the journal held.
		return err
	}
	now := m.cfg.clock.Now()
	var saves []corestm.RecordValue[S]
	//: one pass over the store: records, census and agenda together.
	for entity, err := range m.store.All(ctx) {
		//: a store that fails half-way cannot be reconciled.
		if err != nil {
			//: the machine does not open on a partial view.
			return storeFailure(err, "all")
		}
		key, state := m.store.Key(entity), *m.plan.state(&entity)
		rec, keep := loaded[key]
		delete(loaded, key)
		//: unknown to the journal, or moved behind the machine's back.
		if !keep || rec.State != state {
			rec = corestm.RecordValue[S]{Key: key, State: state, Entered: now, History: rec.History}
			saves = append(saves, rec)
		}
		m.book.adopt(rec)
		m.admit(key, entity, state, rec.Entered, now)
	}
	//: the journal keeps what the reconciliation changed, in one call each.
	return m.syncJournal(ctx, saves, loaded)
}

// loadJournal reads the journal into a map by key; no journal is an empty map.
func (m *StateMachine[E, S]) loadJournal(ctx context.Context) (map[string]corestm.RecordValue[S], error) {
	//: memory only: nothing was kept.
	if m.book.journal == nil {
		//: every entity will enter its state now.
		return make(map[string]corestm.RecordValue[S]), nil
	}
	records, err := m.book.journal.Load(ctx)
	//: a journal that cannot be read fails the opening.
	if err != nil {
		//: the operation names what failed.
		return nil, journalFailure(err, "load")
	}
	out := make(map[string]corestm.RecordValue[S], len(records))
	//: the last record of a key wins, as a replayed journal would have it.
	for _, rec := range records {
		out[rec.Key] = rec
	}
	//: indexed by key.
	return out, nil
}

// syncJournal saves the reconciled records and deletes the orphans — records
// whose entity is no longer in the store.
func (m *StateMachine[E, S]) syncJournal(ctx context.Context, saves []corestm.RecordValue[S], orphans map[string]corestm.RecordValue[S]) error {
	//: memory only.
	if m.book.journal == nil {
		//: nothing to write.
		return nil
	}
	//: one call for every changed record.
	if len(saves) > 0 {
		//: a machine whose journal cannot be written would lose its timers.
		if err := m.book.journal.Save(ctx, saves...); err != nil {
			//: the operation names what failed.
			return journalFailure(err, "save")
		}
	}
	keys := slices.Sorted(maps.Keys(orphans))
	//: nothing to delete.
	if len(keys) == 0 {
		//: reconciled.
		return nil
	}
	//: the same rule as a Save: the opening fails loudly.
	return journalFailure(m.book.journal.Delete(ctx, keys...), "delete")
}

// admit schedules an entity met at opening: its next due instant when a
// transition the loop fires leaves its state. A guard or an instant function
// that panics here leaves the entity for the loop, which reports it.
func (m *StateMachine[E, S]) admit(key string, entity E, state S, entered, now time.Time) {
	//: no timer, no guard: never on the agenda.
	if !m.plan.automatic(state) {
		//: nothing to schedule.
		return
	}
	v, err := m.plan.evaluate(entity, state, entered, now)
	//: the loop will ask again, and report what it finds.
	if err != nil {
		m.agenda.markDirty(key)
		//: left for the loop.
		return
	}
	//: due now: the zero instant sorts before every other.
	if v.fire != nil {
		m.agenda.schedule(key, time.Time{})
		//: the first run fires it.
		return
	}
	//: something falls due later.
	if !v.next.IsZero() {
		m.agenda.schedule(key, v.next)
	}
}

// Census counts the entities per state. Every declared state is present, at
// zero when no entity is in it; an undeclared state found in the store is
// present while an entity is in it.
func (m *StateMachine[E, S]) Census() map[S]int {
	//: a copy the caller owns.
	return m.book.censusCopy()
}

// Record returns what the machine keeps about the entity under key, and false
// when it knows none.
func (m *StateMachine[E, S]) Record(key string) (corestm.RecordValue[S], bool) {
	//: a copy, history included.
	return m.book.lookup(key)
}

// Records returns every record, the most recently entered state first.
func (m *StateMachine[E, S]) Records() []corestm.RecordValue[S] {
	//: copies, sorted.
	return m.book.all()
}

// storeFailure wraps a store error with the operation that failed.
func storeFailure(err error, operation string) error {
	//: origin wins when the store's error is already an SDK error.
	return errs.Wrap(err, errs.WrapParams{
		Code: CodeStoreFailed, Reason: "STORE_FAILED", Public: StoreFailed.Public(), Private: StoreFailed.Private(),
	}, errs.String("operation", operation))
}
