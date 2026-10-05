// Package scheduler — ranges 0.2.12.* (ADR 0041 core/app/scheduler block) and
// 0.3.43.* (ADR 0041 service/app/scheduler block, declared here since ADR 0160).
//
// Package scheduler — declares the sentinel *errs.Error outcomes of the
// domain: the registration refusals, and the parser's refusals of a schedule.
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Package scheduler declares the time-driven execution port of the SDK: the
// [Job] that runs, the [Schedule] that says when, and the [Scheduler] that
// owns the pairing and fires it. A core sibling admitted by ADR 0041.
//
// Both halves of the contract are FUNCTION ports rather than interfaces — the
// shape internal/core/CLAUDE.md already admits for resilience.Operation. Each
// is a single behaviour, so a named func IS the contract and needs no adapter
// at the call site; it is also the narrowest thing pkg/v1 can publish. ADR
// 0039's lesson is that a published port cannot grow a method without breaking
// every downstream implementer at compile time, and a func type cannot grow
// one at all.
//
// The concrete schedules (a POSIX cron expression, a fixed interval) and the
// engine that drives them live in internal/service/app/scheduler; this package
// owns only the contract, the two domain values, and the typed sentinels the
// engine emits. Cron vocabulary deliberately does not appear here: the port
// knows about instants, not about expressions.
//
// Package scheduler — hosts EntryValue, the registration a Scheduler owns.
//
// Package scheduler — hosts ResultValue, the record of one scheduling decision.
package scheduler
