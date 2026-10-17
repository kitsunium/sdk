package workflowengine

import (
	"context"
	"errors"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
	"github.com/kitsunium/sdk/pkg/v1/app/statemachine"
)

// entity is kit's entity as the SDK's engine runs it: the one type it is
// instantiated at (ADR 0179 Q3), whatever the product's entity and state.
type entity = plug.WorkflowEntityValue

// Enable plugs the workflows' engine into the framework.
func Enable() {
	plug.Workflows.Store(&plug.WorkflowDriverConfig{Open: open, Failed: failed})
}

// stateOf is the state of e as the engine reads and sets it: its spelling.
func stateOf(e *entity) *string { return &e.State }

// open opens the SDK's engine over cfg: its definition, its store and its
// journal, the app's clock, and kit's brackets around what the loop fires.
//
// IFACE-PLUGIN: kit reaches a workflow's engine through plug.WorkflowEngine;
// this is its implementation.
func open(ctx context.Context, cfg *plug.WorkflowConfig) (plug.WorkflowEngine, error) {
	sc := &statemachine.Config[entity, string]{
		Store: cfg.Store, Clock: clockOf(cfg.Clock), Actor: cfg.Actor,
		Observe: observeOf(cfg.Observe), Report: cfg.Report, OnLoop: onLoopOf(cfg.OnLoop),
		MinGap: cfg.MinGap, MaxHistory: cfg.MaxHistory,
		Backoff: resilience.Backoff{BaseDelay: cfg.BackoffBase, MaxDelay: cfg.BackoffMax},
	}
	if cfg.Journal != nil {
		sc.Journal = journal{j: cfg.Journal}
	}
	m, err := statemachine.New(ctx, definitionOf(cfg), sc)
	if err != nil {
		return nil, err
	}
	return &engine{m: m}, nil
}

// definitionOf declares cfg's states, transitions and hooks to the SDK's
// engine.
func definitionOf(cfg *plug.WorkflowConfig) *statemachine.Definition[entity, string] {
	def := statemachine.Define(stateOf).Initial(cfg.Initial)
	for i := range cfg.Transitions {
		declare(def, &cfg.Transitions[i])
	}
	for _, h := range cfg.OnEnter {
		def.OnEnter(h.State, h.Fn)
	}
	for _, fn := range cfg.OnTransition {
		def.OnTransition(func(ctx context.Context, c statemachine.Change[entity, string]) error {
			return fn(ctx, changeOf(&c))
		})
	}
	return def
}

// declare declares the transition t to def: a guard's, a deadline's, a
// delay's or an event's.
func declare(def *statemachine.Definition[entity, string], t *plug.TransitionSpec) {
	switch t.Trigger {
	case plug.WorkflowTriggerGuard:
		def.When(t.Event, t.From, t.To, t.Guard)
	case plug.WorkflowTriggerDeadline:
		def.At(t.Event, t.From, t.To, t.Instant)
	case plug.WorkflowTriggerDelay:
		def.After(t.Event, t.Delay, t.From, t.To)
	default:
		def.On(t.Event, t.From, t.To)
	}
}

// sentinels are the SDK's sentinels of each failure kit tells apart.
var sentinels = map[plug.WorkflowFailure]error{
	plug.WorkflowTransitionRefused: statemachine.TransitionRefused,
	plug.WorkflowEntityMissing:     statemachine.EntityMissing,
	plug.WorkflowEntityExists:      statemachine.EntityExists,
	plug.WorkflowKeyEmpty:          statemachine.KeyEmpty,
	plug.WorkflowHookFailed:        statemachine.HookFailed,
	plug.WorkflowHookPanicked:      statemachine.HookPanicked,
	plug.WorkflowHookChangedState:  statemachine.HookChangedState,
	plug.WorkflowHookChangedKey:    statemachine.HookChangedKey,
	plug.WorkflowReentrant:         statemachine.Reentrant,
	plug.WorkflowStoreFailed:       statemachine.StoreFailed,
	plug.WorkflowJournalFailed:     statemachine.JournalFailed,
	plug.WorkflowFunctionPanicked:  statemachine.FunctionPanicked,
	plug.WorkflowLoopPanicked:      statemachine.LoopPanicked,
	plug.WorkflowWaitAbandoned:     statemachine.WaitAbandoned,
}

// failed reports whether err, or an error it wraps, is the SDK's sentinel
// of kind.
func failed(err error, kind plug.WorkflowFailure) bool {
	s, ok := sentinels[kind]
	return ok && errors.Is(err, s)
}
