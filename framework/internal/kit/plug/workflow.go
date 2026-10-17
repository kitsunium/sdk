package plug

import (
	"context"
	"sync/atomic"
	"time"
)

// The workflow subsystem's contract (ADR 0179 §3, step 5): what kit's core
// asks of the engine its workflows run on. framework/kit/workflow sets the
// hook and framework/internal/kit/workflowkit implements it over the SDK's
// statemachine, instantiated once (Q3): over kit's entity — the product's,
// held as any — and the state as its spelling, the name kit's workflow file
// and the Studio write it with. The core reads it and imports none of the
// SDK's statemachine; WorkflowService[E, S] stays the typed facade above it,
// in the core, and wraps what the product declares on its types — the state
// field, the guards, the instants, the hooks — once, into functions of a
// WorkflowEntityValue. The values are here, the engine and the ports it runs
// on in workflow_interface.go.

// WorkflowCreateEvent is the event of a transition Start fires: an entity
// entering its workflow.
const WorkflowCreateEvent string = "create"

// The triggers of a transition.
const (
	// WorkflowTriggerStart is a creation, which Start fires.
	WorkflowTriggerStart WorkflowTrigger = iota
	// WorkflowTriggerEvent is an event code fires.
	WorkflowTriggerEvent
	// WorkflowTriggerDelay is a timer: a duration spent in a state.
	WorkflowTriggerDelay
	// WorkflowTriggerDeadline is a timer at an instant the entity carries.
	WorkflowTriggerDeadline
	// WorkflowTriggerGuard is a condition on the entity that holds.
	WorkflowTriggerGuard
)

// Why a run of a workflow's loop started.
const (
	// WorkflowWakeStart is the loop's first run.
	WorkflowWakeStart WorkflowWake = iota
	// WorkflowWakeDue is a transition coming due.
	WorkflowWakeDue
	// WorkflowWakeChange is a write that may have brought one forward.
	WorkflowWakeChange
)

// What a workflow's loop reports.
const (
	// WorkflowLoopOther is an event of the loop kit shows nothing of.
	WorkflowLoopOther WorkflowLoopEvent = iota
	// WorkflowLoopRunStarted is a run starting: Wake and Started are set.
	WorkflowLoopRunStarted
	// WorkflowLoopRunEnded is a run ending: Started, Ended, Next and what
	// it could not fire, Err, are set.
	WorkflowLoopRunEnded
	// WorkflowLoopWaiting is the loop asleep until Next, zero for no
	// transition due.
	WorkflowLoopWaiting
)

// The engine's failures kit tells apart, which it words for the product.
const (
	// WorkflowFailureNone is no failure kit words.
	WorkflowFailureNone WorkflowFailure = iota
	// WorkflowTransitionRefused is an event that leaves no transition from
	// the entity's state.
	WorkflowTransitionRefused
	// WorkflowEntityMissing is no entity under the key.
	WorkflowEntityMissing
	// WorkflowEntityExists is an entity already under the key Start names.
	WorkflowEntityExists
	// WorkflowKeyEmpty is an entity whose key is empty.
	WorkflowKeyEmpty
	// WorkflowHookFailed is a hook that returned an error, joined to it;
	// its "hook" and "event" fields name the hook's kind and the event.
	WorkflowHookFailed
	// WorkflowHookPanicked is a hook that panicked; its "hook", "panic" and
	// "stack" fields say which and how.
	WorkflowHookPanicked
	// WorkflowHookChangedState is an OnEnter hook that changed the state
	// it entered.
	WorkflowHookChangedState
	// WorkflowHookChangedKey is an OnEnter hook that changed its entity's
	// key.
	WorkflowHookChangedKey
	// WorkflowReentrant is an OnEnter hook that fired its own workflow.
	WorkflowReentrant
	// WorkflowStoreFailed is the workflow's store that failed.
	WorkflowStoreFailed
	// WorkflowJournalFailed is the workflow's journal that failed.
	WorkflowJournalFailed
	// WorkflowFunctionPanicked is a guard or an instant function that
	// panicked.
	WorkflowFunctionPanicked
	// WorkflowLoopPanicked is the loop that panicked.
	WorkflowLoopPanicked
	// WorkflowWaitAbandoned is a caller that stopped waiting for its
	// entity: its context ended.
	WorkflowWaitAbandoned
)

// Workflows is the workflow subsystem: the engine kit's workflows run on;
// nil until framework/kit/workflow is imported. A product that declares a
// workflow without it is refused at the start (ADR 0179 §6).
var Workflows atomic.Pointer[WorkflowDriverConfig]

// WorkflowDriverConfig is what framework/kit/workflow plugs into kit: how a
// workflow's engine opens, and how kit tells the engine's failures apart.
type WorkflowDriverConfig struct {
	// Open opens the engine of one workflow, as cfg declares it: it reads
	// the journal and every entity of the store once.
	Open func(ctx context.Context, cfg *WorkflowConfig) (WorkflowEngine, error)
	// Failed reports whether err, or an error it wraps, is the engine's
	// failure of kind: the SDK's sentinels stay in the subsystem, and the
	// error keeps the fields the engine gave it.
	Failed func(err error, kind WorkflowFailure) bool
}

// WorkflowTrigger is what fires a transition.
type WorkflowTrigger uint8

// WorkflowWake is why a run of a workflow's loop started.
type WorkflowWake uint8

// WorkflowLoopEvent is what a workflow's loop reports.
type WorkflowLoopEvent uint8

// WorkflowFailure is a failure of a workflow's engine, as kit words it.
type WorkflowFailure uint8

// WorkflowConfig is one workflow as its engine runs it: its states and
// transitions, its hooks, the store and the journal it runs over, and how
// the app's loop, clock and spans see it.
type WorkflowConfig struct {
	// Store is the workflow's entities, as the engine reads and writes them.
	Store WorkflowStore
	// Journal keeps what the engine knows of each entity — its state, since
	// when, its latest transitions —; nil keeps it in memory alone.
	Journal WorkflowJournal
	// Clock is the app's clock, which the engine stamps and waits on.
	Clock Clock
	// Actor names, from the context of a transition someone fired, who
	// fired it.
	Actor func(ctx context.Context) string
	// Observe brackets each transition the loop fires: the context it runs
	// with, and the function told how it ended.
	Observe func(ctx context.Context, f WorkflowFiringValue) (context.Context, func(err error))
	// Report receives what the engine reports and no caller's return value
	// carries: an OnTransition hook that failed, a panic, a journal's write
	// that failed.
	Report func(ctx context.Context, err error)
	// OnLoop is told each run of the loop, and each wait.
	OnLoop func(e WorkflowLoopEventValue)
	// Initial is the state Start enters an entity in.
	Initial string
	// Transitions are the declared transitions, in declaration order.
	Transitions []TransitionSpec
	// OnEnter are the hooks run as an entity enters a state, in
	// declaration order.
	OnEnter []EnterHookSpec
	// OnTransition are the hooks run after every stored transition, in
	// declaration order.
	OnTransition []func(ctx context.Context, c WorkflowChangeValue) error
	// MinGap is the least a wake for a due transition waits after a run.
	MinGap time.Duration
	// BackoffBase is the wait after a run that failed, doubled after each,
	// up to BackoffMax.
	BackoffBase time.Duration
	// BackoffMax bounds the wait after a run that failed.
	BackoffMax time.Duration
	// MaxHistory caps the transitions remembered per entity.
	MaxHistory int
}

// TransitionSpec declares one transition: an event's, a timer's after a
// delay or at an instant the entity carries, or a guard's.
type TransitionSpec struct {
	// Instant is when a deadline's transition comes due, false for no
	// instant for now; set for WorkflowTriggerDeadline.
	Instant func(e WorkflowEntityValue) (time.Time, bool)
	// Guard is the condition a guard's transition waits for; set for
	// WorkflowTriggerGuard.
	Guard func(e WorkflowEntityValue) bool
	// Event names the transition.
	Event string
	// From is the state it leaves.
	From string
	// To is the state it enters.
	To string
	// Delay is how long a delay's transition waits in From.
	Delay time.Duration
	// Trigger is what fires it: an event, a delay, a deadline or a guard.
	Trigger WorkflowTrigger
}

// EnterHookSpec is one hook run as an entity enters a state, before it is
// stored: an error cancels the transition.
type EnterHookSpec struct {
	// Fn runs the hook; it may change the entity, not its state nor its
	// key.
	Fn func(ctx context.Context, e *WorkflowEntityValue) error
	// State is the state whose entering runs it.
	State string
}

// WorkflowEntityValue is one entity as a workflow's engine holds it: the
// product's, kept as any — the workflow's typed facade asserts it to its
// type —, and its state as the facade spells it, which the engine reads and
// sets.
type WorkflowEntityValue struct {
	// Value is the product's entity.
	Value any
	// State is the entity's state, spelled.
	State string
}

// WorkflowChangeValue is one stored transition: the entity as stored after
// it, the states it left and entered, what fired it, who and when.
type WorkflowChangeValue struct {
	// At is when it happened.
	At time.Time
	// Entity is the entity, as stored after the transition.
	Entity WorkflowEntityValue
	// Key is the entity's key.
	Key string
	// Event is the transition's name; WorkflowCreateEvent for Start.
	Event string
	// From is the state left, "" on creation.
	From string
	// To is the state entered.
	To string
	// Actor is who fired it, "" for the loop.
	Actor string
	// Trigger is what fired it.
	Trigger WorkflowTrigger
}

// WorkflowFiringValue is one transition the loop is about to fire: the
// entity's key, the event, the states it leaves and enters, and its trigger.
type WorkflowFiringValue struct {
	// Key is the entity's key.
	Key string
	// Event is the transition's name.
	Event string
	// From is the state it leaves.
	From string
	// To is the state it enters.
	To string
	// Trigger is what fires it.
	Trigger WorkflowTrigger
}

// WorkflowRecordValue is what a workflow's engine knows of one entity: its
// state, since when, and its latest transitions, oldest first.
type WorkflowRecordValue struct {
	// Entered is when the entity entered its state.
	Entered time.Time
	// Key is the entity's key.
	Key string
	// State is the entity's state.
	State string
	// History are its latest transitions, oldest first.
	History []WorkflowStepValue
}

// WorkflowStepValue is one transition of an entity's history: the event,
// the states it left and entered, what fired it, who and when.
type WorkflowStepValue struct {
	// At is when it happened.
	At time.Time
	// Event is the transition's name.
	Event string
	// From is the state left, "" on creation.
	From string
	// To is the state entered.
	To string
	// Actor is who fired it, "" for the loop.
	Actor string
	// Trigger is what fired it.
	Trigger WorkflowTrigger
}

// WorkflowLoopEventValue is what a workflow's loop reports: a run started,
// a run ended, a wait.
type WorkflowLoopEventValue struct {
	// Err is what a run could not fire, nil for nothing.
	Err error
	// Started is when the run started.
	Started time.Time
	// Ended is when the run ended.
	Ended time.Time
	// Next is when the loop wakes next, zero for no transition due.
	Next time.Time
	// Kind is what the event reports.
	Kind WorkflowLoopEvent
	// Wake is why the run started.
	Wake WorkflowWake
}
