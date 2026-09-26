//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/statemachine .

// Package statemachine is a state-machine engine over stored entities (ADR
// 0120): declare the states an entity goes through and the transitions between
// them, hand the engine your store, and it moves entities along — on the
// events you fire, and by itself on timers, deadlines and guards.
//
//	def := statemachine.Define(func(o *Order) *Status { return &o.Status }).
//	    Initial(Pending).
//	    On("pay", Pending, Paid).
//	    After("abandon", 24*time.Hour, Pending, Cancelled).    // a duration in the state
//	    At("expire", Paid, Expired, func(o Order) (time.Time, bool) { return o.ShipBy, !o.ShipBy.IsZero() }).
//	    When("ready", Paid, Shipping, func(o Order) bool { return o.Packed }).
//	    OnEnter(Paid, chargeCard).                               // before the store: an error cancels
//	    OnTransition(publish)                                    // after the store: an error is reported
//
//	m, err := statemachine.New(ctx, def, &statemachine.Config[Order, Status]{
//	    Store: orders, Journal: records,
//	    Report: func(ctx context.Context, err error) { log.Print(err) }, // what no return value carries
//	})
//	go func() {
//	    if err := m.Run(ctx); err != nil {                       // LoopRunning: another loop is going
//	        log.Print(err)
//	    }
//	}()                                                          // fires timers, deadlines and guards
//	o, err := m.Start(ctx, Order{ID: "o-1"})                     // Pending, inserted
//	o, err = m.Fire(ctx, "o-1", "pay")                           // Paid, replaced
//
// # Your store is the source of truth
//
// The state is a field of your entity and the entity lives in your [Store]:
// the engine reads it with Get, creates with Insert and moves with Replace — a
// Replace that finds the entity gone stores nothing, so a transition never
// brings back an entity deleted meanwhile. Beside the store the machine keeps
// one [Record] per entity — its state, since when, its latest transitions — in
// memory and in the [Journal] you give it, which is what keeps an After timer
// counting across a restart. Tell the machine about writes it did not make
// with Machine.Changed and Machine.Deleted: a write can move a deadline or
// make a guard hold, and a state changed behind its back enters its record as
// of the moment the machine learns of it.
//
// # Transitions
//
// Transitions of one entity run one at a time, transitions of different
// entities concurrently. A transition runs the OnEnter hooks of the state
// entered, stores the entity, records the step, releases the entity and then
// runs the OnTransition hooks. A hook that panics fails what it was part of —
// an OnEnter hook its transition, an OnTransition hook only itself — and never
// leaves an entity locked. An OnEnter hook may change the entity but not its
// state or key, and must not fire its own machine (Reentrant); an
// OnTransition hook may.
//
// # The loop never polls
//
// Machine.Run keeps an agenda: each entity in a state a timer or a guard
// leaves has one entry, the instant its earliest automatic transition falls
// due, on a heap. The loop fires what is due — per entity, the first declared
// transition due — and sleeps until the next one or until a write wakes it,
// never sooner than Config.MinGap after its last run, so a burst of writes is
// one run. Finding the next transition due costs O(log N) where a loop that
// re-reads the store on every wake pays O(N); BENCH.md in the engine measures
// both. An entity whose transition fails is retried after Config.Backoff,
// counted per entity. Give the machine a clock.ManualClock (pkg/v1/clock) and
// a test drives every timer.
//
// # What it does not do
//
// It is not a durable workflow runtime: there is no replay, no activity, no
// compensation, and nothing survives a crash but what your store and your
// journal hold. A write to an entity that does not go through the machine can
// race with a transition of it; the store's Replace is the arbiter.
package statemachine

import (
	"context"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcstm "github.com/kitsunium/sdk/internal/service/statemachine"
)

// CreateEvent is the event name of the step that brings a new entity into its
// initial state; no declared transition may take it.
const CreateEvent string = corestm.CreateEvent

// DefaultMinGap is the least time between two runs of the loop when
// Config.MinGap is not positive.
const DefaultMinGap time.Duration = svcstm.DefaultMinGap

// DefaultMaxHistory is how many transitions a Record keeps when
// Config.MaxHistory is not positive.
const DefaultMaxHistory int = svcstm.DefaultMaxHistory

// TriggerStart is a Start: the entity entered its initial state.
const TriggerStart Trigger = corestm.TriggerStart

// TriggerEvent is an event a caller fired.
const TriggerEvent Trigger = corestm.TriggerEvent

// TriggerDelay is a duration the entity spent in its state (Definition.After).
const TriggerDelay Trigger = corestm.TriggerDelay

// TriggerDeadline is the arrival of an instant the entity carries
// (Definition.At).
const TriggerDeadline Trigger = corestm.TriggerDeadline

// TriggerGuard is a guard on the entity that held after a write
// (Definition.When).
const TriggerGuard Trigger = corestm.TriggerGuard

// WakeStart is the first run, when Run starts.
const WakeStart Wake = svcstm.WakeStart

// WakeDue is a run the loop woke for by itself: a transition fell due.
const WakeDue Wake = svcstm.WakeDue

// WakeChange is a run a write woke the loop for.
const WakeChange Wake = svcstm.WakeChange

// LoopRunStarted reports a run starting: Wake and Started are set.
const LoopRunStarted LoopEventKind = svcstm.LoopRunStarted

// LoopRunEnded reports a run ending: every field is set.
const LoopRunEnded LoopEventKind = svcstm.LoopRunEnded

// LoopWaiting reports the loop re-armed to wake earlier, at Next, because a
// write arrived while it slept.
const LoopWaiting LoopEventKind = svcstm.LoopWaiting

// CodeTriggerUnknown identifies a name ParseTrigger does not know (0.2.56.1).
const CodeTriggerUnknown errs.Code = corestm.CodeTriggerUnknown

// CodeTransitionRefused identifies an event no declared transition takes from
// the entity's state (0.3.88.1).
const CodeTransitionRefused errs.Code = svcstm.CodeTransitionRefused

// CodeEntityMissing identifies a key the store holds no entity under — never
// there, or deleted while a transition ran (0.3.88.2).
const CodeEntityMissing errs.Code = svcstm.CodeEntityMissing

// CodeEntityExists identifies a Start whose entity's key the store already
// holds (0.3.88.3).
const CodeEntityExists errs.Code = svcstm.CodeEntityExists

// CodeKeyEmpty identifies an entity handed to Start whose key is empty
// (0.3.88.4).
const CodeKeyEmpty errs.Code = svcstm.CodeKeyEmpty

// CodeHookFailed identifies a hook that returned an error (0.3.88.5).
const CodeHookFailed errs.Code = svcstm.CodeHookFailed

// CodeHookPanicked identifies a hook that panicked, recovered as an error
// (0.3.88.6).
const CodeHookPanicked errs.Code = svcstm.CodeHookPanicked

// CodeHookChangedState identifies an OnEnter hook that changed the state it
// was entering (0.3.88.7).
const CodeHookChangedState errs.Code = svcstm.CodeHookChangedState

// CodeHookChangedKey identifies an OnEnter hook that changed its entity's key
// (0.3.88.8).
const CodeHookChangedKey errs.Code = svcstm.CodeHookChangedKey

// CodeReentrant identifies a Start or Fire through the context an OnEnter hook
// of the same machine was given (0.3.88.9).
const CodeReentrant errs.Code = svcstm.CodeReentrant

// CodeStoreFailed identifies a Store method that returned an error
// (0.3.88.10).
const CodeStoreFailed errs.Code = svcstm.CodeStoreFailed

// CodeJournalFailed identifies a Journal method that returned an error
// (0.3.88.11).
const CodeJournalFailed errs.Code = svcstm.CodeJournalFailed

// CodeLoopRunning identifies a Run or a Step asked for while another is going
// (0.3.88.12).
const CodeLoopRunning errs.Code = svcstm.CodeLoopRunning

// CodeFunctionPanicked identifies a guard or an instant function that panicked
// (0.3.88.13).
const CodeFunctionPanicked errs.Code = svcstm.CodeFunctionPanicked

// CodeInitialMissing identifies a definition with no initial state
// (0.3.88.14).
const CodeInitialMissing errs.Code = svcstm.CodeInitialMissing

// CodeEventInvalid identifies a transition declared with an empty event name,
// or CreateEvent (0.3.88.15).
const CodeEventInvalid errs.Code = svcstm.CodeEventInvalid

// CodeTransitionDuplicate identifies a second transition declared for one
// event from one state (0.3.88.16).
const CodeTransitionDuplicate errs.Code = svcstm.CodeTransitionDuplicate

// CodeDelayInvalid identifies an After with a duration that is not positive
// (0.3.88.17).
const CodeDelayInvalid errs.Code = svcstm.CodeDelayInvalid

// CodeFunctionMissing identifies a nil accessor, guard, instant function, hook
// or definition (0.3.88.18).
const CodeFunctionMissing errs.Code = svcstm.CodeFunctionMissing

// CodeStoreMissing identifies a configuration with no store (0.3.88.19).
const CodeStoreMissing errs.Code = svcstm.CodeStoreMissing

// CodeWaitAbandoned identifies a caller whose context ended while the entity
// was busy (0.3.88.20).
const CodeWaitAbandoned errs.Code = svcstm.CodeWaitAbandoned

// CodeLoopPanicked identifies a panic the loop recovered from a store, journal
// or observer call (0.3.88.21).
const CodeLoopPanicked errs.Code = svcstm.CodeLoopPanicked

var (
	// TriggerUnknown refuses a trigger name outside the five.
	TriggerUnknown = corestm.TriggerUnknown

	// TransitionRefused is returned when no transition by that event leaves the
	// entity's state (409); the entity comes back with it.
	TransitionRefused = svcstm.TransitionRefused

	// EntityMissing is returned for a key the store holds no entity under, or one
	// deleted while the transition ran (404).
	EntityMissing = svcstm.EntityMissing

	// EntityExists is returned by a Start over a key already taken (409).
	EntityExists = svcstm.EntityExists

	// KeyEmpty is returned by a Start with an entity whose key is empty.
	KeyEmpty = svcstm.KeyEmpty

	// HookFailed accompanies a hook's own error, joined beside it.
	HookFailed = svcstm.HookFailed

	// HookPanicked is a hook that panicked and was recovered; the value and the
	// stack are log-only fields.
	HookPanicked = svcstm.HookPanicked

	// HookChangedState is an OnEnter hook that changed the state it was entering.
	HookChangedState = svcstm.HookChangedState

	// HookChangedKey is an OnEnter hook that changed its entity's key.
	HookChangedKey = svcstm.HookChangedKey

	// Reentrant is a Start or Fire through the context an OnEnter hook of the same
	// machine was given: refused, not deadlocked.
	Reentrant = svcstm.Reentrant

	// StoreFailed wraps an error a Store method returned.
	StoreFailed = svcstm.StoreFailed

	// JournalFailed wraps an error a Journal method returned.
	JournalFailed = svcstm.JournalFailed

	// LoopRunning is returned by Run or Step while another is going.
	LoopRunning = svcstm.LoopRunning

	// FunctionPanicked is a guard or an instant function that panicked; the entity
	// is retried after its backoff.
	FunctionPanicked = svcstm.FunctionPanicked

	// InitialMissing is returned by New for a definition that never called
	// Initial.
	InitialMissing = svcstm.InitialMissing

	// EventInvalid is a transition declared with an empty event name, or
	// CreateEvent.
	EventInvalid = svcstm.EventInvalid

	// TransitionDuplicate is one event declared twice from one state.
	TransitionDuplicate = svcstm.TransitionDuplicate

	// DelayInvalid is an After with a duration that is not positive.
	DelayInvalid = svcstm.DelayInvalid

	// FunctionMissing is a nil accessor, guard, instant function, hook or
	// definition.
	FunctionMissing = svcstm.FunctionMissing

	// StoreMissing is returned by New when Config.Store is nil.
	StoreMissing = svcstm.StoreMissing

	// WaitAbandoned is returned when the context ended while the entity was busy
	// (503); errors.Is still finds the context's error.
	WaitAbandoned = svcstm.WaitAbandoned

	// LoopPanicked is a panic the loop recovered from a store, journal or observer
	// call: the entity is retried after its backoff, unless it came from the
	// observer's end, once the transition was stored.
	LoopPanicked = svcstm.LoopPanicked
)

// Store is the port your entities are read and written through. It is frozen
// at five methods.
type Store[E any] = corestm.Store[E]

// Journal is the port the machine keeps its records in, beside your store.
// It is frozen at three methods.
type Journal[S comparable] = corestm.Journal[S]

// Record is what the machine keeps about one entity: its state, since when,
// and its latest transitions.
type Record[S comparable] = corestm.RecordValue[S]

// Step is one transition in a Record's history.
type Step[S comparable] = corestm.StepValue[S]

// Trigger says what fired a transition: a start, an event, a delay, a
// deadline or a guard.
type Trigger = corestm.Trigger

// Definition declares a machine: its states, transitions and hooks.
type Definition[E any, S comparable] = svcstm.MachineSpec[E, S]

// Transition is a declared transition, as Definition.Transitions reads it back.
type Transition[S comparable] = svcstm.TransitionValue[S]

// Change is a stored transition, as an OnTransition hook receives it.
type Change[E any, S comparable] = svcstm.ChangeValue[E, S]

// Config parameterises New. Only Store is required.
type Config[E any, S comparable] = svcstm.Config[E, S]

// Firing is a transition the machine's own loop is about to fire, as
// Config.Observe is told it.
type Firing[S comparable] = svcstm.FiringValue[S]

// Machine runs a Definition over a Store. It is safe for concurrent use.
type Machine[E any, S comparable] = svcstm.StateMachine[E, S]

// Wake says why a run of the loop started.
type Wake = svcstm.Wake

// LoopEvent is what Config.OnLoop is told about Run.
type LoopEvent = svcstm.LoopEvent

// LoopEventKind says what a LoopEvent reports.
type LoopEventKind = svcstm.LoopEventKind

// Define starts the declaration of a machine over entities of type E. state
// returns a pointer to the entity's state field.
func Define[E any, S comparable](state func(*E) *S) *Definition[E, S] {
	//: delegate verbatim to the service constructor.
	return svcstm.NewMachineSpec(state)
}

// New checks def and cfg and opens a machine over cfg.Store: it reads the
// journal and every entity once, reconciles them and schedules what is due.
// Every problem of the declaration is returned at once, joined.
func New[E any, S comparable](ctx context.Context, def *Definition[E, S], cfg *Config[E, S]) (*Machine[E, S], error) {
	//: delegate verbatim to the service constructor.
	return svcstm.NewStateMachine(ctx, def, cfg)
}

// ParseTrigger reads a trigger's name — "start", "event", "delay", "deadline"
// or "guard" — as Trigger.String writes it.
func ParseTrigger(name string) (Trigger, error) {
	//: delegate verbatim to the contract.
	return corestm.ParseTrigger(name)
}
