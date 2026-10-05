// Package statemachine — hosts the agenda: when each entity's next automatic
// transition falls due, the entities written since the loop last looked, and
// the backoff of those whose transition failed.
//
// Package statemachine — hosts the blueprint: a definition frozen for one
// machine, indexed for the lookups a transition makes, and the evaluation of
// what is due for an entity.
//
// Package statemachine — hosts the book: what a machine keeps beside the
// store — one record per entity, the census per state, the entities held by a
// read or a transition — and its writes through the Journal, one key at a
// time.
//
// Package statemachine — hosts Config, a machine's construction parameters,
// and the defaults its zero fields resolve to.
//
// Package statemachine — hosts MachineSpec, the declaration of a machine: its
// states, its transitions and its hooks, and what a caller reads back from it.
//
// Package statemachine — hosts the per-entity locks that serialise the
// transitions of one entity, and the context mark that tells a hook's own
// machine apart.
//
// Package statemachine — hosts the machine's own loop: Step, one pass over
// what is due; Run, which paces the passes and sleeps until the next
// transition due or a write, never polling; and what OnLoop is told.
//
// Package statemachine is the state-machine engine over stored entities (ADR
// 0120): a [MachineSpec] declares the states an entity goes through and the
// transitions between them — events a caller fires, timers after a duration in
// a state, deadlines the entity carries, guards on the entity — with hooks on
// the way; a [StateMachine] runs it over the caller's store, keeps a record of each
// entity beside it, and fires the timers and guards from its own loop, which
// sleeps until the next transition due and wakes on a write. The ports it is
// given — the Store, the Journal — are internal/core/app/statemachine's.
//
// # One entity, one transition at a time
//
// A transition takes its entity's lock, reads it again, runs the OnEnter hooks
// of the state entered, writes it — an insert for a creation, a replace
// otherwise, and a replace never brings back an entity deleted meanwhile —
// records the step, releases the lock, and only then runs the OnTransition
// hooks. A hook that panics fails what it was part of and never leaves an
// entity locked. Transitions of different entities run concurrently.
//
// # The agenda
//
// Each entity in a state that a timer or a guard leaves has ONE entry on a
// heap: the instant its earliest automatic transition falls due. The loop
// pops what is due, evaluates only those entities and the ones written since
// it last looked, and sleeps until the heap's top — so finding the next
// transition due costs O(log N), where re-reading the whole store on every
// wake costs O(N). BENCH.md measures both.
//
// Package statemachine — hosts what a store's owner tells a machine about
// writes it did not make, and how the machine brings its records and its
// agenda in line with them.
//
// Package statemachine — hosts the transitions a caller asks for (Start,
// Fire) and the part every transition shares: the hooks, the write, the step
// recorded, and the lock released before the OnTransition hooks.
package statemachine
