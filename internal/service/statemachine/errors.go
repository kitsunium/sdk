// Package statemachine — declares the sentinel *errs.Error outcomes of the
// engine. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
//
// No Public text names a key, an event or a state: those travel as log-only
// fields, because a key is often an identifier a caller would rather not see
// echoed, and the fields are where an operator looks anyway.
package statemachine

import "github.com/kitsunium/sdk/internal/kernel/errs"

// httpNotFound is 404: the entity addressed does not exist.
const httpNotFound int = 404

// httpConflict is 409: the entity's current state, or an existing entity,
// refuses the request.
const httpConflict int = 409

// httpUnavailable is 503: the caller gave up waiting, nothing is wrong with
// the request.
const httpUnavailable int = 503

var (
	// TransitionRefused is returned by Fire for an event no declared
	// transition takes from the entity's current state — including an event
	// that exists but leaves another state, and a timer or guard transition,
	// which only the machine's own loop fires. The entity is returned beside
	// it, so the caller can read the state that refused.
	TransitionRefused = errs.Define(CodeTransitionRefused, "TRANSITION_REFUSED",
		"That transition is not possible from the current state",
		"service/statemachine: no event transition by that name leaves the entity's state; the fields name the event and the state",
		errs.WithHTTPStatus(httpConflict))

	// EntityMissing is returned when the store holds no entity under the key:
	// by Fire for a key never stored, and by any transition whose entity was
	// deleted while it ran — the store's Replace reported it gone, and the
	// transition stored nothing rather than bring it back.
	EntityMissing = errs.Define(CodeEntityMissing, "ENTITY_MISSING",
		"No such entity",
		"service/statemachine: the store holds no entity under the key, or it was deleted while the transition ran; the key field names it",
		errs.WithHTTPStatus(httpNotFound))

	// EntityExists is returned by Start when the store already holds an
	// entity under the new one's key. Nothing was stored and no hook ran
	// after the refusal.
	EntityExists = errs.Define(CodeEntityExists, "ENTITY_EXISTS",
		"An entity with that key already exists",
		"service/statemachine: the store's Insert reported the key taken; the key field names it",
		errs.WithHTTPStatus(httpConflict))

	// KeyEmpty is returned by Start for an entity whose key is empty. The
	// engine locks, records and schedules by key, so an empty one would make
	// every such entity the same entity.
	KeyEmpty = errs.Define(CodeKeyEmpty, "KEY_EMPTY",
		"The entity has no key",
		"service/statemachine: Store.Key returned an empty key for the entity handed to Start")

	// HookFailed accompanies the error a hook returned, joined with it so
	// both errors.Is and errs.HasCode answer: an OnEnter hook's failure fails
	// its transition and is returned; an OnTransition hook's is reported
	// through Config.Report and the transition stands.
	HookFailed = errs.Define(CodeHookFailed, "HOOK_FAILED",
		"A state-machine hook failed",
		"service/statemachine: a hook returned an error, joined beside this one; the fields name the hook, the event and the state")

	// HookPanicked is a hook that panicked. The panic is recovered where the
	// hook ran, so an OnEnter hook fails its transition — and the entity's
	// lock is released on the way out, which is what kept a panicking hook
	// from leaving the machine locked for good. The recovered value and the
	// stack travel as log-only fields.
	HookPanicked = errs.Define(CodeHookPanicked, "HOOK_PANICKED",
		"A state-machine hook panicked",
		"service/statemachine: a hook panicked and was recovered; the fields carry the hook, the event, the value and the stack")

	// HookChangedState refuses a transition whose OnEnter hook changed the
	// entity's state. A hook may change the entity, not where it is going:
	// the history would record one state and the store hold another.
	HookChangedState = errs.Define(CodeHookChangedState, "HOOK_CHANGED_STATE",
		"A state-machine hook changed the state it was entering",
		"service/statemachine: after the OnEnter hooks the entity's state was no longer the transition's target; nothing was stored")

	// HookChangedKey refuses a transition whose OnEnter hook changed the
	// entity's key: storing it would replace, or create, another entity.
	HookChangedKey = errs.Define(CodeHookChangedKey, "HOOK_CHANGED_KEY",
		"A state-machine hook changed the key of its entity",
		"service/statemachine: after the OnEnter hooks Store.Key no longer returned the entity's key; nothing was stored")

	// Reentrant refuses a Start or a Fire made, through the context an
	// OnEnter hook was given, on the machine running that hook. The hook runs
	// while the machine holds its entity's lock, so the call would wait for
	// itself; it is refused instead of deadlocking. Fire from an
	// OnTransition hook, which runs once the lock is released.
	Reentrant = errs.Define(CodeReentrant, "REENTRANT",
		"A state-machine hook cannot fire its own machine",
		"service/statemachine: Start or Fire was called with a context an OnEnter hook of the same machine was given; fire from OnTransition instead")

	// StoreFailed wraps an error a Store method returned. When the store's
	// error is itself an SDK error its code is kept and this one joins the
	// trail, so both errs.HasCode checks answer.
	StoreFailed = errs.Define(CodeStoreFailed, "STORE_FAILED",
		"The state machine's store failed",
		"service/statemachine: a Store method returned an error; the fields name the operation")

	// JournalFailed wraps an error a Journal method returned. Load failing
	// fails the machine's opening; Save and Delete failing are reported and
	// the transition stands, since the store is the source of truth and a
	// lost record only costs a timer its origin after a restart.
	JournalFailed = errs.Define(CodeJournalFailed, "JOURNAL_FAILED",
		"The state machine's journal failed",
		"service/statemachine: a Journal method returned an error; the fields name the operation")

	// LoopRunning refuses a Run or a Step while another Run or Step of the
	// same machine is going: one loop at a time, so an entity takes at most
	// one automatic transition per run.
	LoopRunning = errs.Define(CodeLoopRunning, "LOOP_RUNNING",
		"The state machine's loop is already running",
		"service/statemachine: Run or Step was called while another Run or Step of this machine had not returned")

	// FunctionPanicked is a guard or an instant function that panicked while
	// the loop asked whether a transition was due. The entity is retried
	// after a backoff; the value and the stack travel as log-only fields.
	FunctionPanicked = errs.Define(CodeFunctionPanicked, "FUNCTION_PANICKED",
		"A state-machine guard or deadline function panicked",
		"service/statemachine: a guard or an instant function panicked and was recovered; the fields carry the event, the value and the stack")

	// InitialMissing refuses a definition that never called Initial: Start
	// would have no state to put a new entity in.
	InitialMissing = errs.Define(CodeInitialMissing, "INITIAL_MISSING",
		"The state machine has no initial state",
		"service/statemachine: the definition never called Initial")

	// EventInvalid refuses a transition declared with an empty event name or
	// with the name a creation's history step takes.
	EventInvalid = errs.Define(CodeEventInvalid, "EVENT_INVALID",
		"A transition needs a name, other than the reserved one",
		"service/statemachine: a transition was declared with an empty event name or with core/statemachine.CreateEvent")

	// TransitionDuplicate refuses a second transition for one event from one
	// state: Fire, and the loop's first-declared rule, would have two answers.
	TransitionDuplicate = errs.Define(CodeTransitionDuplicate, "TRANSITION_DUPLICATE",
		"That event already leaves that state",
		"service/statemachine: a transition was declared twice for one event from one state; the fields name them")

	// DelayInvalid refuses an After with a duration that is not positive: a
	// timer due the instant its state is entered is a guard that always
	// holds, and reads as a mistake.
	DelayInvalid = errs.Define(CodeDelayInvalid, "DELAY_INVALID",
		"A timer transition needs a positive duration",
		"service/statemachine: After was declared with a duration of zero or less; the event field names it")

	// FunctionMissing refuses a nil function a definition needs: the state
	// accessor, a guard, an instant function or a hook — and a nil
	// definition handed to NewStateMachine.
	FunctionMissing = errs.Define(CodeFunctionMissing, "FUNCTION_MISSING",
		"The state machine was given no function where it needs one",
		"service/statemachine: a nil state accessor, guard, instant function, hook or definition; the fields say which")

	// StoreMissing refuses a configuration with no store: a machine's
	// entities have to live somewhere.
	StoreMissing = errs.Define(CodeStoreMissing, "STORE_MISSING",
		"The state machine has no store",
		"service/statemachine: Config.Store is nil")

	// LoopPanicked is a panic the loop recovered from the caller's own code —
	// a store method, a journal, an observer — while it looked at one
	// entity. That entity is retried after its backoff and the loop goes on;
	// Start and Fire, which run on the caller's goroutine, let such a panic
	// through, with the entity's lock released. The value and the stack
	// travel as log-only fields.
	LoopPanicked = errs.Define(CodeLoopPanicked, "LOOP_PANICKED",
		"The state machine's loop recovered a panic",
		"service/statemachine: a store, journal or observer call panicked in the loop; the fields carry the key, the value and the stack")

	// WaitAbandoned is returned when the caller's context ended while it
	// waited for another transition of the same entity to finish. Nothing was
	// read or stored.
	WaitAbandoned = errs.Define(CodeWaitAbandoned, "WAIT_ABANDONED",
		"The request ended while the entity was busy",
		"service/statemachine: the context ended before the entity's lock was free; the cause is the context's error",
		errs.WithHTTPStatus(httpUnavailable))
)
