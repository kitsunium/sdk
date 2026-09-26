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
//	m, err := statemachine.New(ctx, def, &statemachine.Config[Order, Status]{Store: orders, Journal: records})
//	go m.Run(ctx)                                               // fires timers, deadlines and guards
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
// state or key, and must not fire its own machine ([Reentrant]); an
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

// The five triggers.
const (
	TriggerStart    Trigger = corestm.TriggerStart
	TriggerEvent    Trigger = corestm.TriggerEvent
	TriggerDelay    Trigger = corestm.TriggerDelay
	TriggerDeadline Trigger = corestm.TriggerDeadline
	TriggerGuard    Trigger = corestm.TriggerGuard
)

// The three reasons a run starts.
const (
	WakeStart  Wake = svcstm.WakeStart
	WakeDue    Wake = svcstm.WakeDue
	WakeChange Wake = svcstm.WakeChange
)

// The three kinds of LoopEvent.
const (
	LoopRunStarted LoopEventKind = svcstm.LoopRunStarted
	LoopRunEnded   LoopEventKind = svcstm.LoopRunEnded
	LoopWaiting    LoopEventKind = svcstm.LoopWaiting
)

// The codes a caller branches on with errs.HasCode.
const (
	CodeTriggerUnknown      errs.Code = corestm.CodeTriggerUnknown
	CodeTransitionRefused   errs.Code = svcstm.CodeTransitionRefused
	CodeEntityMissing       errs.Code = svcstm.CodeEntityMissing
	CodeEntityExists        errs.Code = svcstm.CodeEntityExists
	CodeKeyEmpty            errs.Code = svcstm.CodeKeyEmpty
	CodeHookFailed          errs.Code = svcstm.CodeHookFailed
	CodeHookPanicked        errs.Code = svcstm.CodeHookPanicked
	CodeHookChangedState    errs.Code = svcstm.CodeHookChangedState
	CodeHookChangedKey      errs.Code = svcstm.CodeHookChangedKey
	CodeReentrant           errs.Code = svcstm.CodeReentrant
	CodeStoreFailed         errs.Code = svcstm.CodeStoreFailed
	CodeJournalFailed       errs.Code = svcstm.CodeJournalFailed
	CodeLoopRunning         errs.Code = svcstm.CodeLoopRunning
	CodeFunctionPanicked    errs.Code = svcstm.CodeFunctionPanicked
	CodeInitialMissing      errs.Code = svcstm.CodeInitialMissing
	CodeEventInvalid        errs.Code = svcstm.CodeEventInvalid
	CodeTransitionDuplicate errs.Code = svcstm.CodeTransitionDuplicate
	CodeDelayInvalid        errs.Code = svcstm.CodeDelayInvalid
	CodeFunctionMissing     errs.Code = svcstm.CodeFunctionMissing
	CodeStoreMissing        errs.Code = svcstm.CodeStoreMissing
	CodeWaitAbandoned       errs.Code = svcstm.CodeWaitAbandoned
	CodeLoopPanicked        errs.Code = svcstm.CodeLoopPanicked
)

var (
	// TriggerUnknown refuses a trigger outside the five.
	TriggerUnknown = corestm.TriggerUnknown
	// TransitionRefused: no transition by that event leaves the state (409).
	TransitionRefused = svcstm.TransitionRefused
	// EntityMissing: no entity under the key, or deleted meanwhile (404).
	EntityMissing = svcstm.EntityMissing
	// EntityExists: Start with a key already taken (409).
	EntityExists = svcstm.EntityExists
	// KeyEmpty: Start with an entity whose key is empty.
	KeyEmpty = svcstm.KeyEmpty
	// HookFailed accompanies a hook's own error, joined beside it.
	HookFailed = svcstm.HookFailed
	// HookPanicked: a hook panicked and was recovered.
	HookPanicked = svcstm.HookPanicked
	// HookChangedState: an OnEnter hook changed the state it was entering.
	HookChangedState = svcstm.HookChangedState
	// HookChangedKey: an OnEnter hook changed its entity's key.
	HookChangedKey = svcstm.HookChangedKey
	// Reentrant: an OnEnter hook fired its own machine.
	Reentrant = svcstm.Reentrant
	// StoreFailed: a Store method returned an error.
	StoreFailed = svcstm.StoreFailed
	// JournalFailed: a Journal method returned an error.
	JournalFailed = svcstm.JournalFailed
	// LoopRunning: Run or Step while another is going.
	LoopRunning = svcstm.LoopRunning
	// FunctionPanicked: a guard or an instant function panicked.
	FunctionPanicked = svcstm.FunctionPanicked
	// InitialMissing: the definition never called Initial.
	InitialMissing = svcstm.InitialMissing
	// EventInvalid: an empty event name, or CreateEvent.
	EventInvalid = svcstm.EventInvalid
	// TransitionDuplicate: one event declared twice from one state.
	TransitionDuplicate = svcstm.TransitionDuplicate
	// DelayInvalid: After with a duration that is not positive.
	DelayInvalid = svcstm.DelayInvalid
	// FunctionMissing: a nil accessor, guard, instant function, hook or
	// definition.
	FunctionMissing = svcstm.FunctionMissing
	// StoreMissing: Config.Store is nil.
	StoreMissing = svcstm.StoreMissing
	// WaitAbandoned: the context ended while the entity was busy (503).
	WaitAbandoned = svcstm.WaitAbandoned
	// LoopPanicked: the loop recovered a panic of a store, journal or
	// observer call; that entity is retried after its backoff.
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
