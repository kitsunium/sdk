// Package health — hosts StartupCheckValue, and the table that says what each
// of the three registrations is allowed to express. They are three types and
// not one with a Probe field, because a field is set by distraction and a type
// is not.
package health

import "time"

// The three registrations differ in what they are ALLOWED to express, and the
// differences are absences rather than documentation:
//
//	                     Startup            Readiness          Liveness
//	body                 Check (ctx)        Check (ctx)        SelfCheck (none)
//	may call out         yes                yes                no — no ctx to bound it
//	NonCritical          absent             present            absent
//	MaxAge (cache)       absent             present            absent
//
// Each absence is a decision recorded in the field set:
//
//   - Liveness has no ctx, so a dependency API cannot be assigned to it.
//   - Liveness has no NonCritical, because "somewhat irrecoverable" restarts
//     nothing; the process is beyond saving or it is not.
//   - Liveness has no MaxAge, because a cached liveness answer means a dead
//     process can keep reporting the "alive" it recorded before it died.
//   - Startup has no NonCritical, because a startup check that does not gate
//     startup is not a startup check.
//   - Startup has no MaxAge, because a startup check runs until it passes once
//     and then never runs again — the strongest cache there is.
//
// The other two live beside this one, in health_check_readiness.go and
// health_check_liveness.go.

// StartupCheckValue registers a check that gates the startup probe: a
// migration that must have run, a warm cache that must have filled, a
// configuration that must have been fetched.
//
// A startup check runs until it PASSES ONCE, and is then never run again. That
// latch is per-check, so an expensive one-shot is not repeated because a
// cheaper sibling was still failing.
//
// The zero value is not registrable and is refused by AddStartup
// ([InvalidCheck]) rather than accepted and silently skipped.
type StartupCheckValue struct {
	// Name identifies the check in the report and in every error field. It
	// must be non-empty and unique among startup checks.
	Name string
	// Check is the body. It may reach outside the process; it is given a
	// context carrying its own budget and MUST honour it.
	Check Check
	// Timeout is this check's budget. A non-positive value is an unset field
	// and CLAMPS to the registry's configured default — never to zero, which
	// would turn a forgotten line into a check that fails before it runs
	// (ADR 0031).
	//
	// A startup check is the one place a long budget is normal: a migration
	// is allowed to take a minute, and the startup probe is precisely the
	// signal that tells the orchestrator to keep waiting.
	Timeout time.Duration
}
