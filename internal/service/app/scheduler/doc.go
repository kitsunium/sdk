// Package scheduler — hosts Config, the engine's construction parameters.
//
// Package scheduler provides the concrete half of the scheduling domain: a
// five-field POSIX cron parser, a fixed-interval schedule, and the engine that
// fires core/app/scheduler.Job values through core/app/scheduler.Schedule. ADR 0041.
//
// The engine depends on internal/kernel/clock.Timed, never on package time
// directly, so a test drives cadence, missed deadlines and overlap by moving a
// ManualClock instead of sleeping (ADR 0039).
//
// Cross-OS: 100% portable Go (time, context, sync, sync/atomic, strconv,
// strings). Cron's finest field is the minute, which is deliberately coarser
// than any platform's timer resolution — see CLAUDE.md §What is guaranteed.
//
// Package scheduler — hosts the calendar cursor the cron walk advances.
//
// Package scheduler — hosts entry, the engine's per-registration state.
//
// Package scheduler — hosts Every, the fixed-interval Schedule.
//
// Package scheduler — hosts fieldSpec, the description of one cron field and
// the parser that turns its text into a membership set.
//
// Package scheduler — the parser's refusal helpers. Every rejection names what
// was wrong in structured fields; the Public message stays a fixed literal.
//
// Package scheduler — hosts the run loop: waiting, firing, and draining.
//
// Package scheduler — hosts the engine: registration, and the state Run needs.
package scheduler
