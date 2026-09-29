// Package kit — workflows: a state machine over the entities of one store.
package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Workflow is a state machine over the entities of one store: the states an
// entity goes through, and the arrows between them. It is declared as data —
// which is why the diagram can draw it exactly — and it runs four kinds of
// transitions:
//
//   - event transitions ([Workflow].On), fired by code with [Workflow].Fire;
//   - timer transitions ([Workflow].After), fired by the workflow's own loop
//     once an entity has spent a duration in a state;
//   - timer transitions at an instant the entity carries ([Workflow].At) — a
//     due date, a deadline — fired by the same loop when it comes;
//   - guard transitions ([Workflow].When), fired by the same loop as soon as
//     a condition on the entity holds.
//
// The SDK's state-machine engine runs it (statemachine). Its loop never
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
type Workflow[E any, S comparable] = ikit.WorkflowService[E, S]

// Delay is how long a timer transition waits: a duration, or a setting that
// holds one — read when the delay is needed, so each environment can give
// its own.
type Delay = ikit.Delay

// Change describes one transition, for [Workflow].OnTransition hooks: the
// entity, the event, and the states it left and entered.
type Change[E any, S comparable] = ikit.ChangeEvent[E, S]
