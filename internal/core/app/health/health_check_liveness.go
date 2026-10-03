// Package health — hosts LivenessCheckValue, the registration a dependency
// cannot be written into. See health_check.go for the table of what each of
// the three may say.
package health

import "time"

// LivenessCheckValue registers PROCESS-LOCAL evidence that this process is not
// irrecoverable. A liveness failure restarts the replica.
//
// Its body is a [SelfCheck] — a func() error, with no context — and that is
// the whole mechanism by which a dependency check cannot land here: every API
// that talks to something outside this process takes a context, and a
// SelfCheck has none to give it. `LivenessCheckValue{Check: db.PingContext}`
// does not compile, and neither does any assignment or conversion between the
// two function types.
//
// What a closure can still do, the type cannot prevent — but wrapping a
// dependency call in `func() error { return db.Ping() }`, inside a literal
// spelled Liveness, is a decision in the diff rather than a slip.
//
// The zero value is not registrable and is refused by AddLiveness
// ([InvalidCheck]).
type LivenessCheckValue struct {
	// Name identifies the check in the report and in every error field. It
	// must be non-empty and unique among liveness checks.
	Name string
	// Check is the body: process-local evidence only.
	Check SelfCheck
	// Timeout is this check's budget, clamped exactly as
	// [StartupCheckValue.Timeout] is.
	//
	// A SelfCheck cannot be cancelled — it has no context to cancel — so the
	// budget bounds the WAIT and not the work: the engine stops waiting,
	// reports the check as timed out, and does not start it again until the
	// abandoned run finally returns. A liveness check that can outlive a
	// budget is itself evidence worth reporting.
	Timeout time.Duration
}
