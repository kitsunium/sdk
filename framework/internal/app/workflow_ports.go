package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"iter"
	"slices"
	"sync"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

// The workflow subsystem's engine — the SDK's statemachine, ADR 0179 step
// 5 — reads and writes a workflow's entities through a port over kit's store
// (plug.WorkflowStore), and keeps what it knows of each — the state, since
// when, the latest transitions — in a journal over the workflow's file, the
// file kit always kept, or its table on a database (plug.WorkflowJournal).

// workflowPort is the engine's port over a workflow's store: the facade's
// entities, each with its state spelled. A read inside a transition someone
// fired, and every write, is a span of the store: the edge the diagram draws
// from the workflow. The loop's own reads are not. state is the JSON name of
// the workflow's state field, "" when kit cannot name it: a transition that
// changes only it makes no version.
type workflowPort[E any, S comparable] struct {
	w     *WorkflowService[E, S]
	state string
}

// Key is d's key in the store.
func (p workflowPort[E, S]) Key(d plug.WorkflowEntityValue) string {
	return p.w.store.key(as[E](d.Value))
}

// Get reads the entity key; false when the store holds none.
func (p workflowPort[E, S]) Get(ctx context.Context, key string) (plug.WorkflowEntityValue, bool, error) {
	s := p.w.store
	var e E
	var err error
	if trace.SpanContextFromContext(ctx).IsValid() {
		var v any
		v, err = s.get(ctx, key, model.EdgePersists)
		e = as[E](v)
	} else {
		e, err = s.read(ctx, key)
	}
	if ke, ok := errors.AsType[*Error](err); ok && ke.Code == WireNotFound {
		return p.w.entityValue(e), false, nil
	}
	return p.w.entityValue(e), err == nil, err
}

// Insert writes d unless its key is taken; false when it was.
func (p workflowPort[E, S]) Insert(ctx context.Context, d plug.WorkflowEntityValue) (bool, error) {
	return p.w.store.transitionWrite(ctx, p.w.entityOf(d), plug.WriteInsert, "")
}

// Replace writes d over an entity of its key; false when there was none.
func (p workflowPort[E, S]) Replace(ctx context.Context, d plug.WorkflowEntityValue) (bool, error) {
	return p.w.store.transitionWrite(ctx, p.w.entityOf(d), plug.WriteReplace, p.state)
}

// All yields every entity of the store.
func (p workflowPort[E, S]) All(ctx context.Context) iter.Seq2[plug.WorkflowEntityValue, error] {
	return func(yield func(plug.WorkflowEntityValue, error) bool) {
		all, err := p.w.store.all(ctx)
		if err != nil {
			// A failed read yields its error once, whatever the caller answers.
			if !yield(plug.WorkflowEntityValue{}, err) {
				return
			}
			return
		}
		for _, e := range all {
			if !yield(p.w.entityValue(e), nil) {
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
func (s *storeCore) transitionWrite(ctx context.Context, v any, mode plug.WriteMode, state string) (stored bool, err error) {
	a := s.app()
	if a == nil {
		return false, notRunning(&s.nodeBase)
	}
	name := "Put"
	if mode == plug.WriteInsert {
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
func (s *storeCore) writeTransition(ctx context.Context, eng plug.StoreEngine, v any, mode plug.WriteMode, state string) error {
	if mode == plug.WriteReplace && state != "" && s.revisions > 0 {
		if err := s.stateOnly(ctx, eng, s.keyOf(v), v, state); !errors.Is(err, errNotInPlace) {
			return err
		}
	}
	d, err := s.docOf(v)
	if err != nil {
		return err
	}
	return eng.Write(ctx, d, mode)
}

// transitionStored is a transition's write as the engine answers it: a key
// taken, or an entity gone, is stored false, not an error.
func transitionStored(mode plug.WriteMode, raw, err error) (bool, error) {
	switch {
	case mode == plug.WriteInsert && plug.Refused(raw, plug.RefusalExists),
		mode == plug.WriteReplace && plug.Refused(raw, plug.RefusalNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// sqlJournal keeps the engine's records on the database that keeps the
// workflow's store, in the table <service>__<workflow>__workflow: a record
// is written in its transition's transaction, so that the entity, what its
// OnEnter hooks wrote and its record commit together. It names no entity
// type: the records spell the states.
type sqlJournal struct {
	eng plug.StoreEngine
	// id is the workflow's node, and declared its declared states, by
	// spelling: a record naming another is dropped at load.
	declared map[string]bool
	id       string
}

// Load reads every record of the workflow's journal.
func (j *sqlJournal) Load(ctx context.Context) ([]plug.WorkflowRecordValue, error) {
	all, err := instancesOf(j.eng.List(ctx))
	if err != nil {
		return nil, failure(CodeWorkflowLoad, "WORKFLOW_LOAD", "the workflow's records cannot be read from its database", err,
			errs.String("workflow", j.id))
	}
	out := make([]plug.WorkflowRecordValue, 0, len(all))
	for i := range all {
		if rec, ok := recordOf(all[i].ID, &all[i], j.declared); ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// Save writes records, in the transaction the context carries.
func (j *sqlJournal) Save(ctx context.Context, records ...plug.WorkflowRecordValue) error {
	for i := range records {
		d, err := instanceDoc(instanceOf(&records[i]))
		if err != nil {
			return err
		}
		if err := j.eng.Write(ctx, d, plug.WriteUpsert); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes the records of keys; a record already gone is no error.
func (j *sqlJournal) Delete(ctx context.Context, keys ...string) error {
	for _, key := range keys {
		if err := j.eng.Delete(ctx, key); err != nil && !plug.Refused(err, plug.RefusalNotFound) {
			return err
		}
	}
	return nil
}

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
func (w *WorkflowService[E, S]) openSQLJournal(a *App, r *databaseRun) (*sqlJournal, error) {
	eng, err := openSQLEngine(a, sqlTable{run: r, table: w.journalTable(), store: w.id}, instanceConfig)
	if err != nil {
		return nil, failure(CodeWorkflowLoad, "WORKFLOW_OPEN", "the workflow's records cannot be opened on its database", err,
			errs.String("workflow", w.id), errs.String("database", r.d.name))
	}
	return &sqlJournal{id: w.id, declared: w.declaredStates(), eng: eng}, nil
}

// workflowJournal keeps the engine's records in the workflow's file,
// <service>/<workflow>.workflow.json under the data directory, as kit always
// wrote it: the Studio's instances, keyed by entity. A record naming a state
// the declaration no longer has is dropped at load, and the engine reads the
// entity's state as entered now. A failed write only costs timer precision
// after a restart: the engine reports it, and it is logged. It names no
// entity type: the records spell the states.
type workflowJournal struct {
	data vfs.FullFS
	// declared are the workflow's declared states, by spelling.
	declared map[string]bool
	// recs are the records the file holds, by entity's key.
	recs map[string]model.Instance
	// id is the workflow's node, file its file under data.
	id   string
	file string

	// mu makes one rewrite of the file at a time: the engine writes the
	// records of different entities at once.
	mu sync.Mutex
}

// Load reads the transitions the journal kept.
//
//ktn:allow-unused-param: the plug.WorkflowJournal interface passes a context the local journal does not take
func (j *workflowJournal) Load(_ context.Context) ([]plug.WorkflowRecordValue, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.recs = map[string]model.Instance{}
	raw, err := fs.ReadFile(j.data, j.file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, failure(CodeWorkflowLoad, "WORKFLOW_LOAD", "the workflow's file cannot be read", err, errs.String("workflow", j.id))
	}
	var stored map[string]model.Instance
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, failure(CodeWorkflowLoad, "WORKFLOW_LOAD", "the workflow's file is not a workflow file", err, errs.String("workflow", j.id))
	}
	out := make([]plug.WorkflowRecordValue, 0, len(stored))
	for key, inst := range stored {
		rec, ok := recordOf(key, &inst, j.declared)
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
//ktn:allow-unused-param: the plug.WorkflowJournal interface passes a context the local journal does not take
func (j *workflowJournal) Save(_ context.Context, records ...plug.WorkflowRecordValue) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := range records {
		j.recs[records[i].Key] = instanceOf(&records[i])
	}
	return j.write()
}

// Delete forgets the journal's records of keys.
//
//ktn:allow-unused-param: the plug.WorkflowJournal interface passes a context the local journal does not take
func (j *workflowJournal) Delete(_ context.Context, keys ...string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, key := range keys {
		delete(j.recs, key)
	}
	return j.write()
}

// write rewrites the file, atomically. The caller holds j.mu.
func (j *workflowJournal) write() error {
	raw, err := json.Marshal(j.recs)
	if err != nil {
		return err
	}
	return j.data.WriteAtomic(j.file, raw, 0o600)
}

// declaredStates are the workflow's declared states, by the spelling the
// file and the engine know them by.
func (w *WorkflowService[E, S]) declaredStates() map[string]bool {
	out := make(map[string]bool, len(w.order))
	for _, st := range w.order {
		out[w.spell(st)] = true
	}
	return out
}

// recordOf reads an instance of the file back as the engine's record; false
// when its state is not a declared one. A step naming an undeclared state is
// dropped from the history.
func recordOf(key string, inst *model.Instance, declared map[string]bool) (plug.WorkflowRecordValue, bool) {
	if !declared[inst.State] {
		return plug.WorkflowRecordValue{}, false
	}
	rec := plug.WorkflowRecordValue{Key: key, State: inst.State, Entered: inst.EnteredAt}
	for _, s := range inst.History {
		if !declared[s.To] || (!declared[s.From] && s.Trigger != model.TriggerCreate) {
			continue
		}
		from := s.From
		if !declared[from] {
			from = "" // a creation leaves no state
		}
		rec.History = append(rec.History, plug.WorkflowStepValue{
			Event: s.Event, From: from, To: s.To, Trigger: engineTrigger(s.Trigger), Actor: s.Caller, At: s.At,
		})
	}
	return rec, true
}

// instanceOf is the engine's record as the file and the Studio spell it.
func instanceOf(rec *plug.WorkflowRecordValue) model.Instance {
	inst := model.Instance{ID: rec.Key, State: rec.State, EnteredAt: rec.Entered.UTC()}
	for _, s := range rec.History {
		step := model.Step{Event: s.Event, From: s.From, To: s.To, At: s.At.UTC(), Trigger: kitTrigger(s.Trigger), Caller: s.Actor}
		if s.Trigger == plug.WorkflowTriggerStart {
			step.From = ""
		}
		inst.History = append(inst.History, step)
	}
	return inst
}

// kitTrigger is what fired a transition, as the model says it: the engine's
// delays and deadlines are both timers.
func kitTrigger(t plug.WorkflowTrigger) string {
	switch t {
	case plug.WorkflowTriggerStart:
		return model.TriggerCreate
	case plug.WorkflowTriggerDelay, plug.WorkflowTriggerDeadline:
		return model.TriggerTimer
	case plug.WorkflowTriggerGuard:
		return model.TriggerGuard
	default:
		return model.TriggerEvent
	}
}

// engineTrigger reads the model's trigger back; a timer is read as a delay,
// which only the history shows.
func engineTrigger(t string) plug.WorkflowTrigger {
	switch t {
	case model.TriggerCreate:
		return plug.WorkflowTriggerStart
	case model.TriggerTimer:
		return plug.WorkflowTriggerDelay
	case model.TriggerGuard:
		return plug.WorkflowTriggerGuard
	}
	return plug.WorkflowTriggerEvent
}

// errNotInPlace ends a transition's write in place: it changes more than the
// state, and makes a version.
var errNotInPlace = errs.New(CodeWorkflowNotInPlace, "WORKFLOW_NOT_IN_PLACE", "the transition changes more than the state",
	"app: a transition's write in place changes more than the state, so it is written as a change and makes a version")

// stateOnly writes v over the record under key in place — no version — when
// it changes nothing but the member state names, checked under the store's
// writers' lock against the record as it is: errNotInPlace otherwise, and
// nothing written.
func (s *storeCore) stateOnly(ctx context.Context, eng plug.StoreEngine, key string, v any, state string) error {
	next, err := s.docOf(v)
	if err != nil {
		return errNotInPlace // the engine says why
	}
	_, err = eng.Update(withInPlace(ctx), key, func(cur *plug.DocumentValue) error {
		cv, err := s.valueOf(*cur)
		if err != nil {
			return err
		}
		was, err := json.Marshal(cv)
		if err != nil || !sameBut(was, next.JSON, state) {
			return errNotInPlace
		}
		*cur = next
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
