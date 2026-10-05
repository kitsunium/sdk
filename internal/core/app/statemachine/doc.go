// Package statemachine — ranges 0.2.56.* (ADR 0120 core/app/statemachine
// block) and 0.3.88.* (ADR 0120 service/app/statemachine block, declared here
// since ADR 0160).
//
// Package statemachine — declares the sentinel *errs.Error outcomes of the
// domain: the contract's one refusal, and the engine's outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public text names a key, an event or a state: those travel as log-only
// fields, because a key is often an identifier a caller would rather not see
// echoed, and the fields are where an operator looks anyway.
//
// Package statemachine — hosts the values the Journal port speaks: a record of
// one entity and the steps of its history.
//
// Package statemachine declares the ports of the state-machine domain (ADR
// 0120): the [Store] a machine reads and writes the entities it drives through,
// the [Journal] that keeps beside them what a store does not — when each entity
// entered its state, and the transitions that brought it there — and the
// values those two ports speak ([RecordValue], [StepValue], [Trigger]). The
// engine — declarations, transitions, hooks, the agenda of timers and the loop
// that fires them — lives in internal/service/app/statemachine; this package owns
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
//
// Package statemachine — hosts Trigger, the closed set of things that fire a
// transition, with its text form. Its one refusal, TriggerUnknown, is
// declared in errors.go with the domain's other sentinels.
package statemachine
