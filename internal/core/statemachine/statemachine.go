// Package statemachine declares the ports of the state-machine domain (ADR
// 0120): the [Store] a machine reads and writes the entities it drives through,
// the [Journal] that keeps beside them what a store does not — when each entity
// entered its state, and the transitions that brought it there — and the
// values those two ports speak ([RecordValue], [StepValue], [Trigger]). The
// engine — declarations, transitions, hooks, the agenda of timers and the loop
// that fires them — lives in internal/service/statemachine; this package owns
// only the contract.
//
// # The store stays the single source of truth
//
// An entity's state is a field of the entity, and the entity lives in the
// caller's store, not in the machine. A machine never keeps a second copy of an
// entity: it reads it through [Store.Get], writes it through [Store.Insert] or
// [Store.Replace], and forgets it the moment the caller says it was deleted.
// What the machine does keep — the instant an entity entered its state and a
// bounded history — is not something the entity carries, and it lives in a
// [Journal] so a restart does not reset every timer.
//
// # Missing and existing are answers, not errors
//
// [Store.Get] reports an absent entity with found == false, [Store.Insert] a
// taken key with inserted == false, and [Store.Replace] a vanished entity with
// replaced == false. A store therefore never has to produce a code of this
// domain to say something ordinary, and the machine decides what each answer
// means: a transition refused, an entity that does not exist, a creation that
// collided. An error from a port method is a store that FAILED, nothing else.
//
// # There is no registry
//
// A store is a caller's collection and a journal is where that caller keeps
// its bookkeeping: neither has a name the SDK could resolve, so, like lock
// (ADR 0052) and secret (ADR 0096), the domain has no name->implementation
// table.
package statemachine

import (
	"context"
	"iter"
)

// Store is the port through which a machine reads and writes the entities
// whose state it drives. The caller implements it over its own collection — a
// document store, a table, a map — and hands it to the engine.
//
// Implementations MUST be safe for concurrent use: the engine calls them from
// the goroutine firing a transition and from its own loop at once.
//
// The method set is FROZEN at five (ADR 0039): pkg/v1/statemachine aliases this
// interface, so a sixth method would break every downstream implementation at
// compile time. A new capability arrives as a sibling interface reached by
// type assertion.
type Store[E any] interface {
	// Key returns the key entity is stored under. It must be a pure function
	// of the entity: the engine locks, records and schedules by it, and a
	// transition never changes it.
	Key(entity E) (key string)
	// Get returns the entity stored under key, and found == false — with a
	// nil error — when there is none.
	Get(ctx context.Context, key string) (entity E, found bool, err error)
	// Insert stores entity under a key no entity holds. When one does, it
	// stores nothing and reports inserted == false with a nil error.
	Insert(ctx context.Context, entity E) (inserted bool, err error)
	// Replace stores entity over the entity holding its key. When none does
	// — it was deleted while a transition ran — it stores nothing and reports
	// replaced == false with a nil error: a transition never brings back an
	// entity somebody deleted.
	Replace(ctx context.Context, entity E) (replaced bool, err error)
	// All yields every entity, then stops; an error ends the sequence. The
	// engine ranges over it once, when a machine opens, to reconcile its
	// journal and build its agenda.
	All(ctx context.Context) iter.Seq2[E, error]
}

// Journal keeps, beside the store, what a machine knows about each entity and
// the entity does not carry: the instant it entered its state and the
// transitions that brought it there. Without one the machine keeps it in
// memory, and after a restart every entity re-enters its state at the moment
// the machine opens — which resets every After timer.
//
// Implementations MUST be safe for concurrent use. The engine calls them
// serialised per machine, under the lock that guards its bookkeeping, so an
// implementation needs no ordering of its own: two saves of one key arrive in
// the order they were made.
//
// Save and Delete take several records at once because a machine that opens
// reconciles every entity in one pass; a journal that rewrites a whole file
// then does it once rather than once per entity.
//
// The method set is FROZEN at three (ADR 0039).
type Journal[S comparable] interface {
	// Load returns every record the journal holds. The engine calls it once,
	// when a machine opens.
	Load(ctx context.Context) (records []RecordValue[S], err error)
	// Save stores each record, replacing any record of the same key.
	Save(ctx context.Context, records ...RecordValue[S]) error
	// Delete forgets the records of keys; a key it does not hold is not an
	// error.
	Delete(ctx context.Context, keys ...string) error
}
