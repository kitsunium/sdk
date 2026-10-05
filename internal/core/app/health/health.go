package health

import "context"

// Check is the body of a startup or readiness check: work that may reach
// outside the process, bounded by the context it is given.
//
// It MUST honour ctx. The engine cancels it when the check's own budget
// expires, and a check that ignores that cancellation turns a probe endpoint
// into a place the process can hang — the failure a health domain exists to
// prevent, not to cause.
//
// A nil error means the check passed. Any non-nil error means it failed; the
// error travels verbatim into the report, and only its errs Public half is
// ever rendered into an HTTP body.
type Check func(ctx context.Context) error

// SelfCheck is the body of a LIVENESS check: process-local evidence that this
// process can still do its job.
//
// It takes no context ON PURPOSE. Every API that talks to something outside
// this process wants one, so a SelfCheck cannot hold a dependency call without
// a closure that visibly throws the deadline away — which is a decision
// somebody has to write down, not a slip.
//
// What belongs here: a supervised goroutine that stopped reporting, a queue
// that has not drained in an hour, a deadlock detector, a broken in-memory
// invariant. What does NOT belong here, ever: a database, a cache, a broker,
// an HTTP dependency, a DNS lookup, or a mutex whose holder is waiting on one
// of those.
//
// A nil error means the process is alive.
type SelfCheck func() error
