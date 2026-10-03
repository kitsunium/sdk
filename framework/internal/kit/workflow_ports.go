// Package kit — the ports the workflow engine reaches a store through.
package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"slices"
	"sync"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/statemachine"
	"github.com/kitsunium/sdk/pkg/v1/trace"
	"github.com/kitsunium/sdk/pkg/v1/vfs"
)

// The SDK's state-machine engine reads and writes a workflow's entities
// through a port over kit's store, and keeps what it knows of each — the
// state, since when, the latest transitions — in a journal over the
// workflow's file, the file kit always kept.

// storePort is the engine's port over a workflow's store. A read inside a
// transition someone fired, and every write, is a span of the store: the
// edge the diagram draws from the workflow. The loop's own reads are not.
// state is the JSON name of the workflow's state field, "" when kit cannot
// name it: a transition that changes only it makes no version.
type storePort[E any] struct {
	s     *StoreService[E]
	state string
}

// Key is e's key in the store.
func (p storePort[E]) Key(e E) string { return p.s.key(e) }

// Get reads the entity key; false when the store holds none.
func (p storePort[E]) Get(ctx context.Context, key string) (E, bool, error) {
	var e E
	var err error
	if trace.SpanContextFromContext(ctx).IsValid() {
		e, err = p.s.get(ctx, key, model.EdgePersists)
	} else {
		e, err = p.s.read(ctx, key)
	}
	if ke, ok := errors.AsType[*Error](err); ok && ke.Code == WireNotFound {
		return e, false, nil
	}
	return e, err == nil, err
}

// Insert writes e unless its key is taken; false when it was.
func (p storePort[E]) Insert(ctx context.Context, e E) (bool, error) {
	return p.s.transitionWrite(ctx, e, insertOnly, "")
}

// Replace writes e over an entity of its key; false when there was none.
func (p storePort[E]) Replace(ctx context.Context, e E) (bool, error) {
	return p.s.transitionWrite(ctx, e, replaceOnly, p.state)
}

// All yields every entity of the store.
func (p storePort[E]) All(ctx context.Context) iter.Seq2[E, error] {
	return func(yield func(E, error) bool) {
		all, err := p.s.all(ctx)
		if err != nil {
			var zero E
			// A failed read yields its error once, whatever the caller answers.
			if !yield(zero, err) {
				return
			}
			return
		}
		for _, e := range all {
			if !yield(e, nil) {
				return
			}
		}
	}
}

// transitionWrite stores what a workflow's transition decided: an insert on
// creation, otherwise a replacement, which never brings back an entity
// deleted meanwhile. A key taken, or an entity gone, is an answer — stored
// is false —, not an error; the span still says the write was refused. One
// of the four funnels of a store's writes: a write kit confirms is told to
// the store's watches (watch.go), inside the write's span. On a store that
// keeps revisions, a replacement that changes only the entity's state —
// the member state names — makes no version: the workflow's journal
// records it (ADR 0007 §3).
func (s *StoreService[T]) transitionWrite(ctx context.Context, v T, mode writeMode, state string) (stored bool, err error) {
	a := s.app()
	if a == nil {
		return false, notRunning(&s.nodeBase)
	}
	name := "Put"
	if mode == insertOnly {
		name = "Insert"
	}
	ctx, sp := a.begin(ctx, &spanStart{node: s.id, from: currentNode(ctx), edge: model.EdgePersists, op: model.OpWrite, name: name})
	eng := s.engine()
	if eng == nil {
		err = notRunning(&s.nodeBase)
		sp.end(err)
		return false, err
	}
	ctx, release, err := s.turn(ctx)
	defer release()
	if err != nil {
		sp.end(err)
		return false, err
	}
	ctx = s.outside(ctx)
	key := s.keyOf(v)
	raw := s.writeTransition(ctx, eng, v, mode, state)
	if err = s.said(raw, key, ""); err == nil {
		s.notify(ctx, key, false)
	}
	sp.end(err)
	return transitionStored(mode, raw, err)
}

// writeTransition writes a transition's entity v in mode: in place when it
// changes only the state and the store keeps revisions, else as any write.
func (s *StoreService[T]) writeTransition(ctx context.Context, eng storeEngine[T], v T, mode writeMode, state string) error {
	if mode == replaceOnly && state != "" && s.revisions > 0 {
		if err := s.stateOnly(ctx, eng, s.keyOf(v), v, state); !errors.Is(err, errNotInPlace) {
			return err
		}
	}
	return eng.Write(ctx, v, mode)
}

// transitionStored is a transition's write as the engine answers it: a key
// taken, or an entity gone, is stored false, not an error.
func transitionStored(mode writeMode, raw, err error) (bool, error) {
	switch {
	case mode == insertOnly && errors.Is(raw, docstore.DocumentExists),
		mode == replaceOnly && errors.Is(raw, docstore.DocumentNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// sqlJournal keeps the engine's records on the database that keeps the
// workflow's store, in the table <service>__<workflow>__workflow: a record
// is written in its transition's transaction, so that the entity, what its
// OnEnter hooks wrote and its record commit together.
type sqlJournal[E any, S comparable] struct {
	w   *WorkflowService[E, S]
	eng *sqlEngine[model.Instance]
}

// Load reads every record of the workflow's journal.
func (j *sqlJournal[E, S]) Load(ctx context.Context) ([]statemachine.Record[S], error) {
	all, err := j.eng.List(ctx)
	if err != nil {
		return nil, failure(CodeWorkflowLoad, "WORKFLOW_LOAD", "the workflow's records cannot be read from its database", err,
			errs.String("workflow", j.w.id))
	}
	states := j.w.statesByName()
	out := make([]statemachine.Record[S], 0, len(all))
	for _, inst := range all {
		if rec, ok := recordOf(inst.ID, inst, states); ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// Save writes records, in the transaction the context carries.
func (j *sqlJournal[E, S]) Save(ctx context.Context, records ...statemachine.Record[S]) error {
	for _, rec := range records {
		if err := j.eng.Write(ctx, instanceOf(rec), upsert); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes the records of keys; a record already gone is no error.
func (j *sqlJournal[E, S]) Delete(ctx context.Context, keys ...string) error {
	for _, key := range keys {
		if err := j.eng.Delete(ctx, key); err != nil && !errors.Is(err, docstore.DocumentNotFound) {
			return err
		}
	}
	return nil
}

// instanceKey is a record's key in the journal's table: its entity's.
func instanceKey(inst model.Instance) string { return inst.ID }

// journalTable is the table of the workflow's journal, on the database
// that keeps its store.
func (w *WorkflowService[E, S]) journalTable() string {
	return tableName(w.svc.name, w.name, workflowSuffix)
}

// journalStore is the store the workflow runs over, nil for none.
//
// IFACE-OPAQUE: the store's entity type is the workflow's own; a database's
// tables see it only as where it is placed.
func (w *WorkflowService[E, S]) journalStore() placementSource {
	if w.store == nil {
		return nil
	}
	return w.store
}

// journaled is a workflow, whatever its types, as a database's tables see
// it.
type journaled interface {
	journalStore() placementSource
	journalTable() string
}

// openSQLJournal opens the workflow's journal on r.
func (w *WorkflowService[E, S]) openSQLJournal(a *App, r *databaseRun) (*sqlJournal[E, S], error) {
	ds, err := docstore.OpenSQL(docstore.SQLConfig[model.Instance]{
		Key: instanceKey, Transactor: r.transactor(),
		Dialect: r.d.dialect(), Table: w.journalTable(),
	})
	if err != nil {
		return nil, failure(CodeWorkflowLoad, "WORKFLOW_OPEN", "the workflow's records cannot be opened on its database", err,
			errs.String("workflow", w.id), errs.String("database", r.d.name))
	}
	return &sqlJournal[E, S]{w: w, eng: &sqlEngine[model.Instance]{ds: ds, key: instanceKey, run: r, a: a, store: w.id}}, nil
}

// workflowJournal keeps the engine's records in the workflow's file,
// <service>/<workflow>.workflow.json under the data directory, as kit always
// wrote it: the Studio's instances, keyed by entity. A record naming a state
// the declaration no longer has is dropped at load, and the engine reads the
// entity's state as entered now. A failed write only costs timer precision
// after a restart: the engine reports it, and it is logged.
type workflowJournal[E any, S comparable] struct {
	w    *WorkflowService[E, S]
	data vfs.FullFS
	file string

	// mu makes one rewrite of the file at a time: the engine writes the
	// records of different entities at once.
	mu   sync.Mutex
	recs map[string]model.Instance
}

// Load reads the transitions the journal kept.
//
//ktn:allow-unused-param: the statemachine.Journal interface passes a context the local journal does not take
func (j *workflowJournal[E, S]) Load(_ context.Context) ([]statemachine.Record[S], error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.recs = map[string]model.Instance{}
	raw, err := fs.ReadFile(j.data, j.file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, failure(CodeWorkflowLoad, "WORKFLOW_LOAD", "the workflow's file cannot be read", err, errs.String("workflow", j.w.id))
	}
	var stored map[string]model.Instance
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, failure(CodeWorkflowLoad, "WORKFLOW_LOAD", "the workflow's file is not a workflow file", err, errs.String("workflow", j.w.id))
	}
	states := j.w.statesByName()
	out := make([]statemachine.Record[S], 0, len(stored))
	for key, inst := range stored {
		rec, ok := recordOf(key, inst, states)
		if !ok {
			continue
		}
		j.recs[key] = inst
		out = append(out, rec)
	}
	return out, nil
}

// Save keeps records in the journal.
//
//ktn:allow-unused-param: the statemachine.Journal interface passes a context the local journal does not take
func (j *workflowJournal[E, S]) Save(_ context.Context, records ...statemachine.Record[S]) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, rec := range records {
		j.recs[rec.Key] = instanceOf(rec)
	}
	return j.write()
}

// Delete forgets the journal's records of keys.
//
//ktn:allow-unused-param: the statemachine.Journal interface passes a context the local journal does not take
func (j *workflowJournal[E, S]) Delete(_ context.Context, keys ...string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, key := range keys {
		delete(j.recs, key)
	}
	return j.write()
}

// write rewrites the file, atomically. The caller holds j.mu.
func (j *workflowJournal[E, S]) write() error {
	raw, err := json.Marshal(j.recs)
	if err != nil {
		return err
	}
	return j.data.WriteAtomic(j.file, raw, 0o600)
}

// statesByName maps each declared state to the name the file spells it with.
func (w *WorkflowService[E, S]) statesByName() map[string]S {
	out := make(map[string]S, len(w.order))
	for _, st := range w.order {
		out[fmt.Sprint(st)] = st
	}
	return out
}

// recordOf reads an instance of the file back as the engine's record; false
// when its state is not a declared one. A step naming an undeclared state is
// dropped from the history.
func recordOf[S comparable](key string, inst model.Instance, states map[string]S) (statemachine.Record[S], bool) {
	st, ok := states[inst.State]
	if !ok {
		return statemachine.Record[S]{}, false
	}
	rec := statemachine.Record[S]{Key: key, State: st, Entered: inst.EnteredAt}
	for _, s := range inst.History {
		to, known := states[s.To]
		from, left := states[s.From]
		if !known || (!left && s.Trigger != model.TriggerCreate) {
			continue
		}
		rec.History = append(rec.History, statemachine.Step[S]{
			Event: s.Event, From: from, To: to, Trigger: engineTrigger(s.Trigger), Actor: s.Caller, At: s.At,
		})
	}
	return rec, true
}

// instanceOf is the engine's record as the file and the Studio spell it.
func instanceOf[S comparable](rec statemachine.Record[S]) model.Instance {
	inst := model.Instance{ID: rec.Key, State: fmt.Sprint(rec.State), EnteredAt: rec.Entered.UTC()}
	for _, s := range rec.History {
		step := model.Step{Event: s.Event, From: fmt.Sprint(s.From), To: fmt.Sprint(s.To), At: s.At.UTC(), Trigger: kitTrigger(s.Trigger), Caller: s.Actor}
		if s.Trigger == statemachine.TriggerStart {
			step.From = ""
		}
		inst.History = append(inst.History, step)
	}
	return inst
}

// kitTrigger is what fired a transition, as the model says it: the engine's
// delays and deadlines are both timers.
func kitTrigger(t statemachine.Trigger) string {
	switch t {
	case statemachine.TriggerStart:
		return model.TriggerCreate
	case statemachine.TriggerDelay, statemachine.TriggerDeadline:
		return model.TriggerTimer
	case statemachine.TriggerGuard:
		return model.TriggerGuard
	}
	return model.TriggerEvent
}

// engineTrigger reads the model's trigger back; a timer is read as a delay,
// which only the history shows.
func engineTrigger(t string) statemachine.Trigger {
	switch t {
	case model.TriggerCreate:
		return statemachine.TriggerStart
	case model.TriggerTimer:
		return statemachine.TriggerDelay
	case model.TriggerGuard:
		return statemachine.TriggerGuard
	}
	return statemachine.TriggerEvent
}

// errNotInPlace ends a transition's write in place: it changes more than the
// state, and makes a version.
var errNotInPlace = errs.New(CodeWorkflowNotInPlace, "WORKFLOW_NOT_IN_PLACE", "the transition changes more than the state",
	"kit: a transition's write in place changes more than the state, so it is written as a change and makes a version")

// stateOnly writes v over the record under key in place — no version — when
// it changes nothing but the member state names, checked under the store's
// writers' lock against the record as it is: errNotInPlace otherwise, and
// nothing written.
func (s *StoreService[T]) stateOnly(ctx context.Context, eng storeEngine[T], key string, v T, state string) error {
	next, err := json.Marshal(v)
	if err != nil {
		return errNotInPlace // the engine says why
	}
	_, err = eng.Update(withInPlace(ctx), key, func(cur *T) error {
		was, err := json.Marshal(*cur)
		if err != nil || !sameBut(was, next, state) {
			return errNotInPlace
		}
		*cur = v
		return nil
	})
	return err
}

// sameBut reports whether two JSON objects hold the same members but the one
// named but — as one encoding of one type writes them, member by member.
func sameBut(a, b []byte, but string) bool {
	ma, ok := objectMembers(a)
	if !ok {
		return false
	}
	mb, ok := objectMembers(b)
	if !ok {
		return false
	}
	drop := func(ms []jsonMember) []jsonMember {
		name := []byte(but)
		return slices.DeleteFunc(ms, func(m jsonMember) bool { return bytes.Equal(m.name, name) })
	}
	ma, mb = drop(ma), drop(mb)
	return slices.EqualFunc(ma, mb, func(x, y jsonMember) bool {
		return bytes.Equal(x.name, y.name) && bytes.Equal(x.value, y.value)
	})
}
