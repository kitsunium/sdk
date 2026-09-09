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
// engine that drives them live in internal/service/scheduler; this package
// owns only the contract, the two domain values, and the typed sentinels the
// engine emits. Cron vocabulary deliberately does not appear here: the port
// knows about instants, not about expressions.
package scheduler

import (
	"context"
	"time"
)

// Job is the unit of work a [Scheduler] fires. It MUST honour ctx: the
// scheduler passes the context Run was called with, so cancelling that context
// is how a shutdown reaches a running job. A Job that ignores ctx delays Run's
// return by its own duration — the scheduler waits for it rather than
// abandoning it, because a job still running after the caller believes the
// scheduler stopped is worse than a slow shutdown.
//
// A Job's error is reported through [ResultValue] and is otherwise ignored: a
// failing job never stops the scheduler and never affects another entry.
type Job func(ctx context.Context) error

// Schedule reports the first instant STRICTLY after `after` at which a job is
// due, and whether such an instant exists at all. A false second return means
// the schedule will never fire again; the scheduler disarms that entry and
// keeps running the others.
//
// Implementations MUST be pure — the same `after` always yields the same
// answer — and MUST be safe for concurrent use. The scheduler calls Next
// repeatedly, including several times in one pass when fires were missed, and
// a Schedule returning a value that is not strictly after its argument would
// make that loop spin forever.
type Schedule func(after time.Time) (next time.Time, ok bool)

// Scheduler owns a set of named [EntryValue] pairings and fires them.
// Implementations MUST be safe for concurrent use.
//
// IFACE-PLUGIN: the concrete engine stays unexported behind its constructor in
// internal/service/scheduler.
type Scheduler interface {
	// Add registers an entry. It is refused while Run is executing
	// ([SchedulerRunning]), on a name already registered ([DuplicateJob]), and
	// on an entry that is not runnable — empty name, nil Job, nil Schedule
	// ([InvalidEntry]).
	Add(entry EntryValue) error
	// Run drives the registered entries until ctx is cancelled, then waits for
	// every in-flight job to return and reports nil. A Scheduler with no entry
	// is legitimate and simply waits for ctx: only an unrunnable ENTRY is
	// refused, never an empty entry set. Run is itself refused with
	// [SchedulerRunning] while already running; once it returns, the Scheduler
	// may be added to and run again.
	Run(ctx context.Context) error
}
