package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// maxHistory caps the transitions remembered per instance.
const maxHistory int = 20

// The workflow's own loop ------------------------------------------------

// autoSchedule is the human text of what wakes a workflow's own loop.
const autoSchedule = "at the next transition due · on every write"

// WorkflowService is a state machine over the entities of one store: the states an
// entity goes through, and the arrows between them. It is declared as data —
// which is why the diagram can draw it exactly — and it runs four kinds of
// transitions:
//
//   - event transitions ([WorkflowService.On]), fired by code with [WorkflowService.Fire];
//   - timer transitions ([WorkflowService.After]), fired by the workflow's own loop
//     once an entity has spent a duration in a state;
//   - timer transitions at an instant the entity carries ([WorkflowService.At]) — a
//     due date, a deadline — fired by the same loop when it comes;
//   - guard transitions ([WorkflowService.When]), fired by the same loop as soon as
//     a condition on the entity holds.
//
// The SDK's state-machine engine runs it (statemachine), which the workflow
// subsystem plugs in (framework/app/workflow, ADR 0179 step 5). Its loop never
// polls: it keeps an agenda of the transitions due, sleeps until the
// earliest, and a write of the store — which may bring one forward — wakes
// it. A workflow with nothing due does not run at all. Transitions of one
// entity run one at a time, of different entities at once.
//
// The entity's state lives in the entity itself (the state function returns
// a pointer to it), so the store stays the single source of truth; the
// engine keeps, beside it, when each entity entered its state and the
// history of its transitions — in the workflow's file under the data
// directory, so a timer keeps counting across a restart.
type WorkflowService[E any, S comparable] struct {
	nodeBase
	store   *StoreService[E]
	state   func(*E) *S
	initial S
	hasInit bool
	order   []S
	arrows  []arrow[E, S]
	enter   map[S][]hook[E]
	after   []changeHook[E, S]

	// spelled maps each state the workflow met to its spelling — the name
	// the engine, the workflow's file and the Studio know it by
	// (fmt.Sprint): the engine is instantiated once, over the spelling
	// (ADR 0179 Q3), and the facade reads a state back from it.
	spelled sync.Map

	// mu guards what the running app built: the engine, its loop, and the
	// function that takes the workflow's hooks off its store.
	mu      sync.Mutex
	machine plug.WorkflowEngine
	running *autoRun
	unwatch func()
}

// arrow is one declared transition. Its trigger says which of delay or
// delaySetting (After), instant (At) and guard (When) is set.
type arrow[E any, S comparable] struct {
	event        string
	from, to     S
	trigger      string
	delay        time.Duration
	delaySetting *SettingService[time.Duration]
	instant      func(E) (time.Time, bool)
	guard        func(E) bool
	// fnAt is where instant or guard is, for the diagram.
	fnAt *pos
	at   pos
}

// Delay is how long a timer transition waits: a duration, or a setting that
// holds one — read when the delay is needed, so each environment can give
// its own.
type Delay interface {
	time.Duration | *SettingService[time.Duration]
}

type hook[E any] struct {
	fn func(context.Context, *E) error
	at *pos
}

type changeHook[E any, S comparable] struct {
	fn func(context.Context, ChangeEvent[E, S]) error
	at *pos
}

// ChangeEvent describes one transition, for [WorkflowService.OnTransition] hooks: the
// entity, the event, and the states it left and entered.
type ChangeEvent[E any, S comparable] struct {
	// Key is the entity's key in the store.
	Key string
	// Entity is the entity, as stored after the transition.
	Entity E
	// Event is the transition's name; "create" for [WorkflowService.Start].
	Event string
	// From is the state left; the zero S on creation.
	From S
	// To is the state entered.
	To S
	// Trigger is "create", "event", "timer" or "guard".
	Trigger string
	// Caller is the node that fired it, when code did.
	Caller string
	// At is when it happened.
	At time.Time
}

// transitionOf keys, in the context handed to the engine, who fired a
// transition and of which entity: the engine records the first as the
// transition's caller, and a report of an OnTransition hook names the second.
type transitionOf struct{}

// firing is what transitionOf keys.
type firing struct{ caller, key string }

// autoRun is the engine's loop running in an app.
type autoRun struct {
	a      *App
	state  *loopState
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// autoLoop is what the app needs of a workflow to run its own loop.
type autoLoop interface {
	hasAuto() bool
	startLoop(a *App, state *loopState)
	stopLoop(ctx context.Context) error
}

// wait is how long a timer transition waits: its setting's value in the app
// that runs it, or the duration it was declared with.
func (x *arrow[E, S]) wait() time.Duration {
	if x.delaySetting != nil {
		return x.delaySetting.Get()
	}
	return x.delay
}

// Workflow declares a state machine over the entities of store. state returns
// a pointer to the entity's state field.
//
//go:noinline
func (s *Service) Workflow[E any, S comparable](name string, store *StoreService[E], state func(*E) *S) *WorkflowService[E, S] {
	w := NewWorkflowService(store, state)
	w.kind, w.name, w.decl = model.KindWorkflow, name, callerPos()
	s.add(w, true)
	if store == nil || state == nil {
		s.problem(w.decl, w.id, "workflow.needs", "name", name)
		return w
	}
	// The state is the workflow's: a restore of a version keeps it, and a
	// transition that changes only it makes no version (ADR 0007 §3).
	store.states = append(store.states, w.stateField)
	return w
}

// see records states in the order the declarations name them first.
func (w *WorkflowService[E, S]) see(states ...S) {
	for _, st := range states {
		if !slices.Contains(w.order, st) {
			w.order = append(w.order, st)
		}
	}
}

// Initial sets the state a new entity enters with [WorkflowService.Start].
func (w *WorkflowService[E, S]) Initial(state S) *WorkflowService[E, S] {
	w.initial, w.hasInit = state, true
	w.see(state)
	return w
}

// On declares an event transition: [WorkflowService.Fire] with this event moves an
// entity in state from to state to. One event may leave several states, but
// only one arrow per (event, from).
//
//go:noinline
func (w *WorkflowService[E, S]) On(event string, from, to S) *WorkflowService[E, S] {
	return w.arrow(arrow[E, S]{event: event, from: from, to: to, trigger: model.TriggerEvent, at: callerPos()})
}

// After declares a timer transition: an entity that has spent d in state from
// moves to state to, fired by the workflow's own loop. d is a duration, or a
// setting of the service that holds one — which must then be positive.
//
//go:noinline
func (w *WorkflowService[E, S]) After[D Delay](event string, d D, from, to S) *WorkflowService[E, S] {
	x := arrow[E, S]{event: event, from: from, to: to, trigger: model.TriggerTimer, at: callerPos()}
	switch v := any(d).(type) {
	case time.Duration:
		if v <= 0 {
			w.svc.problem(x.at, w.id, "workflow.timer-positive", "name", w.name, "event", event)
		}
		x.delay = v
	case *SettingService[time.Duration]:
		if v == nil {
			w.svc.problem(x.at, w.id, "workflow.timer-nil-setting", "name", w.name, "event", event)
			break
		}
		v.waitedBy = append(v.waitedBy, say("setting.waiter", "event", event, "workflow", w.id))
		x.delaySetting = v
	}
	return w.arrow(x)
}

// At declares a timer transition at an instant the entity carries — a due
// date, a deadline: an entity in state from moves to state to once the time
// instant returns has come; false means no instant for now. kit asks again
// each time the entity is written, and the workflow's own loop sleeps until
// the earliest instant.
//
//go:noinline
func (w *WorkflowService[E, S]) At(event string, from, to S, instant func(E) (time.Time, bool)) *WorkflowService[E, S] {
	a := arrow[E, S]{event: event, from: from, to: to, trigger: model.TriggerTimer, instant: instant, at: callerPos()}
	if p, _ := funcInfo(instant); p.file() != "" {
		a.fnAt = &p
	}
	if instant == nil {
		w.svc.problem(a.at, w.id, "workflow.instant-nil", "name", w.name, "event", event)
	}
	return w.arrow(a)
}

// When declares a guard transition: an entity in state from moves to state to
// as soon as guard holds. The guard sees the entity only: kit checks it each
// time the entity is written, never on a clock — a condition that depends on
// the time is an instant, declared with [WorkflowService.At].
//
//go:noinline
func (w *WorkflowService[E, S]) When(event string, from, to S, guard func(E) bool) *WorkflowService[E, S] {
	a := arrow[E, S]{event: event, from: from, to: to, trigger: model.TriggerGuard, guard: guard, at: callerPos()}
	if p, _ := funcInfo(guard); p.file() != "" {
		a.fnAt = &p
	}
	if guard == nil {
		w.svc.problem(a.at, w.id, "workflow.guard-nil", "name", w.name, "event", event)
	}
	return w.arrow(a)
}

// arrow adds a transition, refusing a second one from the same state on the
// same event.
func (w *WorkflowService[E, S]) arrow(a arrow[E, S]) *WorkflowService[E, S] {
	for _, x := range w.arrows {
		if x.event == a.event && x.from == a.from {
			w.svc.problem(a.at, w.id, "workflow.event-twice", "name", w.name, "event", a.event, "state", a.from)
			return w
		}
	}
	if a.event == "" || a.event == "create" {
		w.svc.problem(a.at, w.id, "workflow.event-name", "name", w.name, "event", a.event)
		return w
	}
	w.arrows = append(w.arrows, a)
	w.see(a.from, a.to)
	return w
}

// OnEnter runs fn when an entity enters state, before it is stored. An error
// cancels the transition, and so does a panic — reported as
// [CodeWorkflowHookPanic], the entity free for the next transition. fn may
// change the entity, not its state nor its key ([CodeWorkflowHookChange]),
// and must not fire this workflow, which is refused
// ([CodeWorkflowReentrant]): transitions of one entity run one at a time.
func (w *WorkflowService[E, S]) OnEnter(state S, fn func(context.Context, *E) error) *WorkflowService[E, S] {
	h := hook[E]{fn: fn}
	if p, _ := funcInfo(fn); p.file() != "" {
		h.at = &p
	}
	w.enter[state] = append(w.enter[state], h)
	w.see(state)
	return w
}

// OnTransition runs fn after every transition has been stored, whoever fired
// it — the natural place to publish it on a topic. An error or a panic is
// logged and shown by the Studio; the transition stands, and the next hooks
// still run.
func (w *WorkflowService[E, S]) OnTransition(fn func(context.Context, ChangeEvent[E, S]) error) *WorkflowService[E, S] {
	h := changeHook[E, S]{fn: fn}
	if p, _ := funcInfo(fn); p.file() != "" {
		h.at = &p
	}
	w.after = append(w.after, h)
	return w
}

// engine is the running engine, or nil.
func (w *WorkflowService[E, S]) engine() plug.WorkflowEngine {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.machine
}

// Start enters a new entity into the workflow: it sets the initial state,
// runs the OnEnter hooks, and inserts the entity in the store.
func (w *WorkflowService[E, S]) Start(ctx context.Context, e E) (E, error) {
	a := w.app()
	if a == nil {
		return e, notRunning(&w.nodeBase)
	}
	caller := currentNode(ctx)
	ctx, sp := a.begin(ctx, &spanStart{node: w.id, from: caller, edge: model.EdgeTransitions, label: "create", op: model.OpTransition, name: "create"})
	out, err := e, notRunning(&w.nodeBase)
	if m := w.engine(); m != nil {
		key := w.store.keyOf(e)
		err = w.inTransaction(ctx, a, func(ctx context.Context) error {
			d, err := m.Start(context.WithValue(ctx, transitionOf{}, firing{caller: caller, key: key}), w.entityValue(e))
			out = w.entityOf(d)
			return err
		})
		if err != nil {
			out, err = e, w.refusal(ctx, a, err, key, plug.WorkflowCreateEvent, e)
		}
	}
	sp.end(err)
	return out, err
}

// inTransaction runs a transition Start or Fire asks for in one transaction
// (ADR 0004) — the caller's when it runs in one, a savepoint of it —: the
// entity, what its OnEnter hooks wrote and its record commit together, and
// its OnTransition hooks run once the outermost transaction committed. On
// the data directory and in memory the transaction takes the writer turn
// before the engine takes the entity: a write is its outermost lock.
func (w *WorkflowService[E, S]) inTransaction(ctx context.Context, a *App, run func(context.Context) error) error {
	return transact(ctx, a, func(ctx context.Context) error {
		if w.store.local() && !w.store.apart() {
			if err := unitOf(ctx).claimLocal(ctx, a, w.store.id); err != nil {
				return err
			}
		}
		return run(ctx)
	})
}

// Fire moves the entity with the given key along the event transition
// leaving its current state. It is a [Conflict] error when no such
// transition exists, and a [NotFound] error when the entity does not.
func (w *WorkflowService[E, S]) Fire(ctx context.Context, key, event string) (E, error) {
	a := w.app()
	if a == nil {
		var zero E
		return zero, notRunning(&w.nodeBase)
	}
	caller := currentNode(ctx)
	ctx, sp := a.begin(ctx, &spanStart{node: w.id, from: caller, edge: model.EdgeTransitions, label: event, op: model.OpTransition, name: event})
	e, err := w.fire(ctx, a, key, event, caller)
	sp.attr("instance", clip(key))
	sp.end(err)
	return e, err
}

// fire applies event to the entity key, as caller.
func (w *WorkflowService[E, S]) fire(ctx context.Context, a *App, key, event, caller string) (E, error) {
	m := w.engine()
	if m == nil {
		var zero E
		return zero, notRunning(&w.nodeBase)
	}
	var e E
	err := w.inTransaction(ctx, a, func(ctx context.Context) error {
		d, err := m.Fire(context.WithValue(ctx, transitionOf{}, firing{caller: caller, key: key}), key, event)
		e = w.entityOf(d)
		return err
	})
	if err != nil {
		return e, w.refusal(ctx, a, err, key, event, e)
	}
	return e, nil
}

// refusal is what Start or Fire returns for the engine's err: a transition
// not possible from the entity's state is a [Conflict] naming the state, a
// hook's panic is logged with its stack; the rest is said as the loop says it.
func (w *WorkflowService[E, S]) refusal(ctx context.Context, a *App, err error, key, event string, e E) error {
	switch {
	case workflowFailed(err, plug.WorkflowTransitionRefused):
		return Conflict(fmt.Sprintf("%s: %q is not possible from state %q", w.name, clip(event), fmt.Sprint(*w.state(&e))))
	case workflowFailed(err, plug.WorkflowHookPanicked):
		w.logPanic(ctx, a, err)
	}
	return w.said(err, key, event)
}

// said is the engine's err in the words kit always used: an entity missing
// is a [NotFound], a key taken a [Conflict], an empty key [Invalid]; an
// OnEnter hook's own error comes back as it returned it; a panic is kit's
// failure — its value and its stack go to the log (logPanic), never into
// the error — and a store's refusal is the store's.
func (w *WorkflowService[E, S]) said(err error, key, event string) error {
	if err == nil {
		return nil
	}
	if isHook, hookErr := w.hookSaid(err, event); isHook {
		return hookErr
	}
	switch {
	case workflowFailed(err, plug.WorkflowEntityMissing):
		return w.store.missing(key)
	case workflowFailed(err, plug.WorkflowEntityExists):
		return Conflict(fmt.Sprintf("%s: an entity with key %q already exists", w.store.name, clip(key)))
	case workflowFailed(err, plug.WorkflowKeyEmpty):
		return Invalid(fmt.Sprintf("%s: an entity's key cannot be empty", w.store.name))
	case workflowFailed(err, plug.WorkflowWaitAbandoned):
		// The caller stopped waiting for the entity: its context's own error.
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return context.Canceled
	}
	if ke, isKit := errors.AsType[*Error](err); isKit {
		return ke
	}
	return err
}

// hookSaid is kit's word for a failure of a hook, of a function or of the
// workflow's loop; isHook is false for any other error.
func (w *WorkflowService[E, S]) hookSaid(err error, event string) (isHook bool, said error) {
	switch {
	case workflowFailed(err, plug.WorkflowHookPanicked):
		return true, hookPanicked(w.id, hookName(fieldOf(err, "hook")))
	case workflowFailed(err, plug.WorkflowHookFailed):
		return true, hookOwn(err)
	case workflowFailed(err, plug.WorkflowFunctionPanicked), workflowFailed(err, plug.WorkflowLoopPanicked):
		return true, failure(CodeLoopPanic, "LOOP_PANICKED", "the workflow's loop panicked", nil, errs.String("workflow", w.id))
	case workflowFailed(err, plug.WorkflowHookChangedState), workflowFailed(err, plug.WorkflowHookChangedKey):
		return true, failure(CodeWorkflowHookChange, "HOOK_CHANGED", "an OnEnter hook changed the state it entered, or its entity's key", nil,
			errs.String("workflow", w.id), errs.String("event", event))
	case workflowFailed(err, plug.WorkflowReentrant):
		return true, failure(CodeWorkflowReentrant, "WORKFLOW_REENTRANT", "an OnEnter hook fired its own workflow", nil, errs.String("workflow", w.id))
	default:
		return false, nil
	}
}

// hookOwn is the error an OnEnter or OnTransition hook returned, out of the
// engine's account of it.
func hookOwn(err error) error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if !workflowFailed(e, plug.WorkflowHookFailed) {
				return e
			}
		}
	}
	return err
}

// hookPanicked is the error of a workflow hook that panicked; hook is
// OnEnter or OnTransition.
func hookPanicked(workflow, hook string) error {
	return failure(CodeWorkflowHookPanic, "HOOK_PANICKED", "a workflow hook panicked", nil,
		errs.String("workflow", workflow), errs.String("hook", hook))
}

// hookName is a hook's kind as the engine names it, as kit's API names it.
func hookName(engine string) string {
	switch engine {
	case "on-enter":
		return "OnEnter"
	case "on-transition":
		return "OnTransition"
	}
	return engine
}

// logPanic logs what panicked — a hook, a guard, an instant function — with
// the value and the stack the engine kept.
func (w *WorkflowService[E, S]) logPanic(ctx context.Context, a *App, err error) {
	w.logPanicked(ctx, a, workflowFailed(err, plug.WorkflowHookPanicked), fieldOf(err, "hook"), fieldOf(err, "panic"), fieldOf(err, "stack"))
}

// logPanicked logs a panic of a hook — hook says which, as the engine names
// it — or of the loop, with its value and its stack.
func (w *WorkflowService[E, S]) logPanicked(ctx context.Context, a *App, ofHook bool, hook, value, stack string) {
	msg := "a workflow's loop panicked"
	if ofHook {
		msg = "a workflow hook panicked"
	}
	logger.Error(ctx, a.log, msg, logger.String("node", w.id), logger.String("hook", hookName(hook)),
		logger.String("panic", value), logger.String("stack", stack))
}

// report receives what the engine reports and no caller's return value
// carries: an OnTransition hook that failed or panicked — a problem the
// Studio shows, the transition standing —, a panic in the loop — logged with
// its stack —, a write of the workflow's file that failed — logged. What the
// loop could not fire also reaches the end of its run (onLoop).
func (w *WorkflowService[E, S]) report(ctx context.Context, a *App, err error) {
	if workflowFailed(err, plug.WorkflowJournalFailed) {
		logger.Warn(ctx, a.log, "a workflow's file could not be written; its timers count from the next start after a restart",
			logger.String("node", w.id), logger.String("error", err.Error()))
		return
	}
	panicked := isPanic(err)
	if !panicked && !workflowFailed(err, plug.WorkflowHookFailed) {
		return
	}
	if panicked {
		w.logPanic(ctx, a, err)
	}
	hook, event := hookFields(err)
	if hook != "on-transition" {
		return
	}
	key := fieldOf(err, "key")
	if f, ok := ctx.Value(transitionOf{}).(firing); ok && key == "" {
		key = f.key
	}
	w.transitionHookFailed(a, event, key, w.said(err, key, event))
}

// transitionHookFailed is the problem the Studio shows for an OnTransition
// hook of the transition event of the entity key that failed as said says.
func (w *WorkflowService[E, S]) transitionHookFailed(a *App, event, key string, said error) {
	_, body := describe(said)
	a.problem(w.id, say("workflow.hook-failed", "workflow", w.id, "event", event, "key", key, "detail", body.Message))
}

// isPanic reports whether err is a panic of a workflow's hook, function or
// loop.
func isPanic(err error) bool {
	return workflowFailed(err, plug.WorkflowHookPanicked) || workflowFailed(err, plug.WorkflowFunctionPanicked) || workflowFailed(err, plug.WorkflowLoopPanicked)
}

// hookFields are the hook kind and the event the engine's account of a
// hook names.
func hookFields(err error) (hook, event string) {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if workflowFailed(e, plug.WorkflowHookFailed) {
				return fieldOf(e, "hook"), fieldOf(e, "event")
			}
		}
	}
	return fieldOf(err, "hook"), fieldOf(err, "event")
}

// Census counts the entities per state; every declared state is present.
// The engine keeps the count; the call is drawn, as the diagram declares it,
// as a read of the store.
func (w *WorkflowService[E, S]) Census(ctx context.Context) (map[S]int, error) {
	a := w.app()
	if a == nil {
		return nil, notRunning(&w.nodeBase)
	}
	_, sp := a.begin(ctx, &spanStart{node: w.store.id, from: currentNode(ctx), edge: model.EdgeReads, op: model.OpRead, name: "Census"})
	m := w.engine()
	if m == nil {
		err := notRunning(&w.nodeBase)
		sp.end(err)
		return nil, err
	}
	counts := w.count(m.Census())
	sp.end(nil)
	return counts, nil
}

// count is the engine's census, by spelling, as the facade's states, with
// every declared state present.
func (w *WorkflowService[E, S]) count(census map[string]int) map[S]int {
	counts := make(map[S]int, len(w.order))
	for _, st := range w.order {
		counts[st] = 0
	}
	for name, n := range census {
		counts[w.stateNamed(name)] += n
	}
	return counts
}

// spell is the spelling of st, the name the engine knows it by — and the
// workflow's file and the Studio —, which the facade reads st back from.
func (w *WorkflowService[E, S]) spell(st S) string {
	name := fmt.Sprint(st)
	if _, met := w.spelled.Load(name); !met {
		w.spelled.LoadOrStore(name, st)
	}
	return name
}

// stateNamed is the state spelled name, the zero S for a spelling the
// workflow never met — the state a creation leaves.
func (w *WorkflowService[E, S]) stateNamed(name string) S {
	st, _ := w.spelled.Load(name)
	return as[S](st)
}

// entityValue is e as the engine holds it: e, and its state spelled.
func (w *WorkflowService[E, S]) entityValue(e E) plug.WorkflowEntityValue {
	return plug.WorkflowEntityValue{Value: e, State: w.spell(*w.state(&e))}
}

// entityOf is the entity the engine holds, d, with the state the engine set:
// a transition sets the spelling, which the entity's state field follows.
func (w *WorkflowService[E, S]) entityOf(d plug.WorkflowEntityValue) E {
	e := as[E](d.Value)
	if d.Value == nil || w.state == nil {
		return e
	}
	if st := w.state(&e); fmt.Sprint(*st) != d.State {
		if named, met := w.spelled.Load(d.State); met {
			*st = as[S](named)
		}
	}
	return e
}

// stateField finds which field of the entity the state function points at,
// by address, and returns its wire name — "" when it points elsewhere.
func (w *WorkflowService[E, S]) stateField() (name string) {
	if w.state == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			name = "" // a state function that cannot run on a zero entity
		}
	}()
	var e E
	v := reflect.ValueOf(&e).Elem()
	if v.Kind() != reflect.Struct {
		return ""
	}
	target := reflect.ValueOf(w.state(&e)).Pointer()
	for _, f := range reflect.VisibleFields(v.Type()) {
		fv, err := v.FieldByIndexErr(f.Index)
		if err != nil || f.Anonymous || !fv.CanAddr() || fv.Addr().Pointer() != target || f.Type != reflect.TypeFor[S]() {
			continue
		}
		name, _ := jsonName(&f)
		return name
	}
	return ""
}

// jsonName is the member encoding/json writes f as; written is false when it
// writes none.
func jsonName(f *reflect.StructField) (name string, written bool) {
	switch tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag {
	case "-":
		return "", false
	case "":
		return f.Name, true
	default:
		return tag, true
	}
}

// stateType returns the qualified name of the state type and its declared
// values: every schema of that type can then list them.
func (w *WorkflowService[E, S]) stateType() (string, []string) {
	values := make([]string, len(w.order))
	for i, st := range w.order {
		values[i] = fmt.Sprint(st)
	}
	name, _ := typeName(reflect.TypeFor[S]())
	return name, values
}

// hasAuto reports whether the workflow has timer or guard transitions, and
// so needs its loop.
func (w *WorkflowService[E, S]) hasAuto() bool {
	for _, x := range w.arrows {
		if x.trigger != model.TriggerEvent {
			return true
		}
	}
	return false
}

// Can reports whether event may be fired on e.
func (w *WorkflowService[E, S]) Can(e E, event string) bool {
	cur := *w.state(&e)
	for _, x := range w.arrows {
		if x.event == event && x.from == cur && x.trigger == model.TriggerEvent {
			return true
		}
	}
	return false
}

// publishCensus tells the Studio how many entities are in each state.
func (w *WorkflowService[E, S]) publishCensus(a *App) {
	if !a.hub.enabled {
		return
	}
	m := w.engine()
	if m == nil {
		return
	}
	counts := map[string]int{}
	for st, n := range w.count(m.Census()) {
		counts[fmt.Sprint(st)] = n
	}
	a.hub.publish(model.Event{Type: model.EventCensus, Census: &model.Census{Workflow: w.id, Counts: counts}})
}

// transitioned streams a stored transition to the Studio — the transition,
// then the census — before the product's OnTransition hooks run.
func (w *WorkflowService[E, S]) transitioned(a *App, c *plug.WorkflowChangeValue) {
	from := c.From
	if c.Trigger == plug.WorkflowTriggerStart {
		from = ""
	}
	a.hub.publish(model.Event{Type: model.EventTransition, Time: c.At.UTC(), Transition: &model.TransitionEvent{
		Workflow: w.id, Instance: c.Key, Event: c.Event, From: from, To: c.To, Trigger: kitTrigger(c.Trigger), Caller: c.Actor,
	}})
	w.publishCensus(a)
}

// changeOf is the engine's stored transition as an OnTransition hook reads
// it: the entity and the states the facade's.
func (w *WorkflowService[E, S]) changeOf(c *plug.WorkflowChangeValue) ChangeEvent[E, S] {
	return ChangeEvent[E, S]{
		Key: c.Key, Entity: w.entityOf(c.Entity), Event: c.Event, From: w.stateNamed(c.From), To: w.stateNamed(c.To),
		Trigger: kitTrigger(c.Trigger), Caller: c.Actor, At: c.At.UTC(),
	}
}

// spec is the transition x as the engine declares it: a guard's, an
// instant's, a timer's — its delay as its setting holds it in this run — or
// an event's; a guard and an instant function read the facade's entity.
func (w *WorkflowService[E, S]) spec(x *arrow[E, S]) plug.TransitionSpec {
	t := plug.TransitionSpec{Event: x.event, From: w.spell(x.from), To: w.spell(x.to), Trigger: plug.WorkflowTriggerEvent}
	switch guard, instant := x.guard, x.instant; {
	case guard != nil:
		t.Trigger = plug.WorkflowTriggerGuard
		t.Guard = func(d plug.WorkflowEntityValue) bool { return guard(w.entityOf(d)) }
	case instant != nil:
		t.Trigger = plug.WorkflowTriggerDeadline
		t.Instant = func(d plug.WorkflowEntityValue) (time.Time, bool) { return instant(w.entityOf(d)) }
	case x.trigger == model.TriggerTimer:
		t.Trigger, t.Delay = plug.WorkflowTriggerDelay, x.wait()
	}
	return t
}

// enterHook is the OnEnter hook fn as the engine runs it: on the facade's
// entity, whose state field holds the state entered, the entity and its
// state — whatever fn did to them — handed back to the engine.
func (w *WorkflowService[E, S]) enterHook(fn func(context.Context, *E) error) func(context.Context, *plug.WorkflowEntityValue) error {
	return func(ctx context.Context, d *plug.WorkflowEntityValue) error {
		e := w.entityOf(*d)
		err := fn(ctx, &e)
		*d = w.entityValue(e)
		return err
	}
}

// config declares the workflow to the engine: its states, its transitions —
// a timer's delay as its setting holds it in this run — and its hooks, kit's
// own OnTransition hook first; the ports and the brackets are start's.
func (w *WorkflowService[E, S]) config(a *App) *plug.WorkflowConfig {
	for _, st := range w.order {
		w.spell(st)
	}
	cfg := &plug.WorkflowConfig{Initial: w.spell(w.initial)}
	for i := range w.arrows {
		cfg.Transitions = append(cfg.Transitions, w.spec(&w.arrows[i]))
	}
	for _, st := range w.order {
		for _, h := range w.enter[st] {
			cfg.OnEnter = append(cfg.OnEnter, plug.EnterHookSpec{State: w.spell(st), Fn: w.enterHook(h.fn)})
		}
	}
	// A transition runs in a transaction: its hooks wait for the commit,
	// kit's own — the Studio's events — as the product's.
	cfg.OnTransition = append(cfg.OnTransition, func(ctx context.Context, c plug.WorkflowChangeValue) error {
		if !hold(ctx, heldEffect{node: w.id, release: func(context.Context) error { w.transitioned(a, &c); return nil }, inside: true}) {
			w.transitioned(a, &c)
		}
		return nil
	})
	for _, h := range w.after {
		cfg.OnTransition = append(cfg.OnTransition, w.heldHook(a, h.fn))
	}
	return cfg
}

// heldHook is a product's OnTransition hook fn as the engine runs it: held
// until the transition's transaction commits.
func (w *WorkflowService[E, S]) heldHook(a *App, fn func(context.Context, ChangeEvent[E, S]) error) func(context.Context, plug.WorkflowChangeValue) error {
	return func(ctx context.Context, c plug.WorkflowChangeValue) error {
		if hold(ctx, heldEffect{node: w.id, release: func(rctx context.Context) error { w.afterCommit(rctx, a, fn, &c); return nil }}) {
			return nil
		}
		return fn(ctx, w.changeOf(&c))
	}
}

// afterCommit runs an OnTransition hook the transition's transaction held,
// once it committed, with the context the release gives it — the
// transition's values, the committing transaction's life: its error, or its
// panic, is reported as the engine reports a hook's, and the transition
// stands.
func (w *WorkflowService[E, S]) afterCommit(ctx context.Context, a *App, fn func(context.Context, ChangeEvent[E, S]) error, c *plug.WorkflowChangeValue) {
	defer func() {
		if p := recover(); p != nil {
			w.logPanicked(ctx, a, true, "on-transition", fmt.Sprint(p), string(debug.Stack()))
			w.transitionHookFailed(a, c.Event, c.Key, hookPanicked(w.id, hookName("on-transition")))
		}
	}()
	if err := fn(ctx, w.changeOf(c)); err != nil {
		w.transitionHookFailed(a, c.Event, c.Key, err)
	}
}

// observe makes each transition the engine's loop fires a span of its own —
// the instance, and what triggered it — and one transaction (ADR 0004): the
// entity, what its OnEnter hooks wrote and its record commit together when
// the engine says the transition stood, and its OnTransition hooks run once
// they did. A transition that lost a race — its entity deleted meanwhile —
// is not a failure.
func (w *WorkflowService[E, S]) observe(a *App) func(context.Context, plug.WorkflowFiringValue) (context.Context, func(error)) {
	return func(ctx context.Context, f plug.WorkflowFiringValue) (context.Context, func(error)) {
		tctx, sp := a.begin(ctx, &spanStart{node: w.id, label: f.Event, op: model.OpTransition, name: f.Event})
		sp.attr("instance", f.Key)
		sp.attr("trigger", kitTrigger(f.Trigger))
		tctx = context.WithValue(tctx, transitionOf{}, firing{key: f.Key})
		tctx, tx := beginTransaction(tctx, a)
		return tctx, func(err error) {
			if cerr := tx.finish(tctx, err); err == nil {
				err = cerr
			}
			if workflowFailed(err, plug.WorkflowEntityMissing) {
				err = nil
			}
			sp.end(w.said(err, f.Key, f.Event))
		}
	}
}

// startLoop runs the engine's loop; state is its entry in the Runtime view.
// Everything the loop fires runs on its goroutine, labelled as the loop's.
func (w *WorkflowService[E, S]) startLoop(a *App, state *loopState) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(a.baseCtx))
	r := &autoRun{a: a, state: state, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	w.mu.Lock()
	w.running = r
	m := w.machine
	w.mu.Unlock()
	go func() {
		done := r.done
		defer close(done)
		if m == nil {
			return
		}
		if err := m.Run(a.asLoop(ctx, state.Name)); err != nil {
			logger.Error(ctx, a.log, "a workflow's loop did not run", logger.String("node", w.id), logger.String("error", err.Error()))
		}
	}()
}

// stopLoop stops the loop and waits for the run going on, if any.
func (w *WorkflowService[E, S]) stopLoop(ctx context.Context) error {
	w.mu.Lock()
	r := w.running
	w.running = nil
	w.mu.Unlock()
	if r == nil {
		return nil
	}
	r.cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.state.setState(r.a, model.LoopStopped)
	return nil
}

// loop is the running loop, or nil.
func (w *WorkflowService[E, S]) loop() *autoRun {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

// onLoop draws the engine's loop as the workflow's loop in the Runtime view:
// each run, why it started, what failed in it, when the loop wakes next.
func (w *WorkflowService[E, S]) onLoop(a *App) func(plug.WorkflowLoopEventValue) {
	return func(e plug.WorkflowLoopEventValue) {
		r := w.loop()
		if r == nil {
			return
		}
		switch e.Kind {
		case plug.WorkflowLoopRunStarted:
			r.state.begin(a, wakeReason(e.Wake))
		case plug.WorkflowLoopWaiting:
			r.state.idle(a, e.Next)
		case plug.WorkflowLoopRunEnded:
			err := w.loopFailure(e.Err)
			if r.ctx.Err() != nil && errors.Is(err, context.Canceled) {
				err = nil // the app is stopping: the run honoured its context
			}
			if err != nil {
				logger.Warn(r.ctx, a.log, "a workflow's transitions failed", logger.String("node", w.id), logger.String("error", err.Error()))
			}
			r.state.idle(a, e.Next)
			r.state.ran(a, e.Started, e.Ended, err)
		default:
			// Every other event changes nothing the Studio shows.
		}
	}
}

// loopFailure is what a run of the loop could not fire, each failure said
// as kit says it.
func (w *WorkflowService[E, S]) loopFailure(err error) error {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return w.said(err, fieldOf(err, "key"), fieldOf(err, "event"))
	}
	var out []error
	for _, e := range joined.Unwrap() {
		out = append(out, w.said(e, fieldOf(e, "key"), fieldOf(e, "event")))
	}
	return errors.Join(out...)
}

// wakeReason is why the engine's loop ran, as the model says it.
func wakeReason(wake plug.WorkflowWake) string {
	switch wake {
	case plug.WorkflowWakeDue:
		return model.WakeDeadline
	case plug.WorkflowWakeChange:
		return model.WakeChange
	default:
		return model.WakeStart
	}
}

// start opens the SDK's engine over the store: it reads the workflow's file
// and every entity once, and an entity changed behind the workflow's back
// re-enters its state now. From then on, every write and every deletion of
// the store reaches the engine: a write may move a deadline or make a guard
// hold, a state changed by a plain write is recorded, a deleted entity is
// forgotten.
func (w *WorkflowService[E, S]) start(ctx context.Context, a *App) error {
	d := plug.Workflows.Load()
	if d == nil || d.Open == nil {
		return workflowsMissing()
	}
	cfg := w.config(a)
	cfg.Store = workflowPort[E, S]{w: w, state: w.stateField()}
	cfg.Clock = pluggedClock{Timed: a.clock}
	cfg.Actor = func(ctx context.Context) string {
		f, _ := ctx.Value(transitionOf{}).(firing)
		return f.caller
	}
	cfg.Observe = w.observe(a)
	cfg.Report = func(ctx context.Context, err error) { w.report(ctx, a, err) }
	cfg.OnLoop = w.onLoop(a)
	cfg.MinGap, cfg.MaxHistory = minDeadlineGap, maxHistory
	cfg.BackoffBase, cfg.BackoffMax = loopBackoff.BaseDelay, loopBackoff.MaxDelay
	switch r := a.databaseOf(w.store); {
	case r != nil:
		j, err := w.openSQLJournal(a, r)
		if err != nil {
			return err
		}
		cfg.Journal = j
	case a.data != nil && !w.store.inMemory:
		cfg.Journal = &workflowJournal{id: w.id, declared: w.declaredStates(), data: a.data, file: path.Join(w.svc.name, w.name+".workflow.json")}
	}
	// The engine is set before a write can reach it: a write between its
	// read of the store and now waits here, then tells it. A start that fails
	// takes its hooks back: a retried Start adds them once.
	w.mu.Lock()
	defer w.mu.Unlock()
	unwatch := w.store.watch(w.changed, w.forget)
	m, err := d.Open(ctx, cfg)
	if err != nil {
		unwatch()
		return w.openFailure(err)
	}
	w.machine, w.unwatch = m, unwatch
	return nil
}

// openFailure is why the engine did not open: the workflow's file or the
// store, as they said it, or the engine's own refusal.
func (w *WorkflowService[E, S]) openFailure(err error) error {
	if errs.HasCode(err, CodeWorkflowLoad) || workflowFailed(err, plug.WorkflowStoreFailed) || workflowFailed(err, plug.WorkflowJournalFailed) {
		return err
	}
	return failure(CodeWorkflowLoad, "WORKFLOW_OPEN", "the workflow cannot start", err, errs.String("workflow", w.id))
}

// changed tells the engine an entity was written.
func (w *WorkflowService[E, S]) changed(key string) {
	m, a := w.engine(), w.app()
	if m == nil || a == nil {
		return
	}
	if err := m.Changed(context.Background(), key); err != nil {
		w.report(context.Background(), a, err)
	}
}

// forget tells the engine an entity was deleted, and streams the census.
func (w *WorkflowService[E, S]) forget(key string) {
	m, a := w.engine(), w.app()
	if m == nil || a == nil {
		return
	}
	if err := m.Deleted(context.Background(), key); err != nil {
		w.report(context.Background(), a, err)
	}
	w.publishCensus(a)
}

// stop takes the workflow's own hooks off its store — another workflow over
// the same store keeps its — and lets go of the engine.
func (w *WorkflowService[E, S]) stop(_ context.Context, _ *App) error {
	w.mu.Lock()
	unwatch := w.unwatch
	w.machine, w.unwatch = nil, nil
	w.mu.Unlock()
	if unwatch != nil {
		unwatch()
	}
	return nil
}

// check reports what can only be judged once the declaration is complete.
func (w *WorkflowService[E, S]) check() []phrase {
	var out []phrase
	if !w.hasInit {
		out = append(out, say("workflow.initial", "name", w.name))
	}
	// The engine knows a state by its spelling (ADR 0179 Q3): two declared
	// states spelled alike would be one state to it.
	spelled := make(map[string]bool, len(w.order))
	for _, st := range w.order {
		name := fmt.Sprint(st)
		if spelled[name] {
			out = append(out, say("workflow.state-spelled-twice", "name", w.name, "state", name))
		}
		spelled[name] = true
	}
	return out
}

// instanceList returns the instances, most recently changed first.
func (w *WorkflowService[E, S]) instanceList() []model.Instance {
	m := w.engine()
	if m == nil {
		return []model.Instance{}
	}
	records := m.Records()
	out := make([]model.Instance, len(records))
	for i := range records {
		out[i] = instanceOf(&records[i])
	}
	slices.SortStableFunc(out, func(a, b model.Instance) int { return b.EnteredAt.Compare(a.EnteredAt) })
	return out
}

// describe fills the graph node out with what the Workflow declares, and
// returns its edges.
func (w *WorkflowService[E, S]) describe(a *App, out *model.Node) []model.Edge {
	info := &model.WorkflowInfo{Initial: fmt.Sprint(w.initial), Field: w.stateField()}
	if w.store != nil {
		info.Store = w.store.id
	}
	var counts map[S]int
	if m := w.engine(); a != nil && a.running() && m != nil {
		counts = w.count(m.Census())
	}
	for _, st := range w.order {
		info.States = append(info.States, w.stateInfo(a, st, counts))
	}
	for i := range w.arrows {
		info.Transitions = append(info.Transitions, w.arrows[i].info(a))
	}
	out.Workflow = info
	if w.store == nil {
		return nil
	}
	return []model.Edge{{From: w.id, To: w.store.id, Kind: model.EdgePersists, Declared: true}}
}

// stateInfo is what the graph says of the state st: whether it is terminal,
// its OnEnter hooks, and — while the app runs — how many entities are in it.
func (w *WorkflowService[E, S]) stateInfo(a *App, st S, counts map[S]int) model.StateInfo {
	si := model.StateInfo{Name: fmt.Sprint(st), Terminal: !slices.ContainsFunc(w.arrows, func(x arrow[E, S]) bool { return x.from == st })}
	for _, h := range w.enter[st] {
		if src := a.source(h.at); src != nil {
			si.OnEnter = append(si.OnEnter, *src)
		}
	}
	if counts != nil {
		si.Count = new(counts[st])
	}
	return si
}

// info is what the graph says of the transition: its event, its states, what
// triggers it and where it is declared.
func (x *arrow[E, S]) info(a *App) model.TransitionInfo {
	ti := model.TransitionInfo{Event: x.event, From: fmt.Sprint(x.from), To: fmt.Sprint(x.to), Trigger: x.trigger, Source: a.source(&x.at)}
	switch {
	case x.guard != nil:
		ti.Guard = a.source(x.fnAt)
	case x.instant != nil:
		ti.At = a.source(x.fnAt)
	case x.trigger == model.TriggerTimer:
		ti.After = x.wait().String()
		if x.delaySetting != nil {
			ti.Setting = x.delaySetting.name
		}
	}
	return ti
}

// NewWorkflowService is a workflow no service declares yet, over the
// entities of store whose state state reads: [Service.Workflow] makes one and
// declares it, which is how a product gets one.
func NewWorkflowService[E any, S comparable](store *StoreService[E], state func(*E) *S) *WorkflowService[E, S] {
	return &WorkflowService[E, S]{store: store, state: state, enter: map[S][]hook[E]{}}
}
