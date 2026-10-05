package statemachine

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/app/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// NewStateMachine checks def and cfg, then opens a machine over cfg.Store: it
// reads the journal, reads every entity once, reconciles the two — an entity
// the journal does not know, or whose state differs, enters its state now; a
// record whose entity is gone is dropped — and schedules what is due. It is
// the only moment the machine reads the whole store.
//
// A definition with problems is refused with every problem joined, a missing
// Initial as [corestm.InitialMissing]; a nil or storeless cfg with [corestm.StoreMissing]; a
// journal that cannot be loaded, or a store that cannot be read, with
// [corestm.JournalFailed] or [corestm.StoreFailed]. The machine keeps its own copy of def,
// and reads cfg once.
func NewStateMachine[E any, S comparable](ctx context.Context, def *MachineSpec[E, S], cfg *Config[E, S]) (*StateMachine[E, S], error) {
	//: a nil definition has nothing to run.
	if def == nil {
		//: named like every other missing function.
		return nil, errs.Wrap(corestm.FunctionMissing, errs.WrapParams{}, errs.String("function", "definition"))
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
		problems = append(problems, corestm.InitialMissing)
	}
	//: a store is required.
	if cfg.Store == nil {
		problems = append(problems, corestm.StoreMissing)
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
		Code: corestm.CodeStoreFailed, Reason: "STORE_FAILED", Public: corestm.StoreFailed.Public(), Private: corestm.StoreFailed.Private(),
	}, errs.String("operation", operation))
}
