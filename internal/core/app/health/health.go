// Package health declares the SDK's process-health port: the three probes an
// orchestrator asks — startup, readiness, liveness — and the checks that
// answer them. A core sibling admitted by ADR 0060.
//
// # Liveness and readiness are not the same signal
//
// This package exists because confusing them causes a cascade. A saturated
// service is not a dead service. If a liveness probe fails because a
// dependency is slow, the orchestrator KILLS the replica; the traffic it was
// carrying moves to the replicas that remain, which are now closer to the same
// saturation, and fail the same probe. The cause of the outage is the probe.
//
// So the distinction is STRUCTURAL here, not documentary. The three probes do
// not take the same registration type, and the liveness one cannot express a
// dependency call:
//
//   - [StartupCheckValue] and [ReadinessCheckValue] carry a [Check], which
//     takes a context.Context — the argument every dependency API asks for
//     (sql.DB.PingContext, net.Dialer.DialContext, http.NewRequestWithContext).
//   - [LivenessCheckValue] carries a [SelfCheck], which takes NOTHING. The two
//     function types are not assignable in either direction, so
//     health.LivenessCheckValue{Check: db.PingContext} does not compile.
//
// A liveness check answers one question — "is this process irrecoverable, so
// that only a restart can fix it?" — and nothing outside the process can
// contribute to that answer.
//
// # The three states
//
//   - Startup: the process is coming up. Do not kill it, do not route to it.
//     While any registered startup check has yet to pass, the liveness probe
//     reports healthy WITHOUT running anything and the readiness probe reports
//     not-ready without running anything.
//   - Readiness: can this replica take traffic right now? Dependency checks
//     live here. A failure removes the replica from routing; it never restarts
//     it.
//   - Liveness: is the process irrecoverable? Process-local evidence only. A
//     failure restarts the replica.
//
// # Draining
//
// [Health.Drain] makes readiness report not-ready permanently while liveness
// keeps answering. That ordering is the point: an orchestrator removes the
// replica from routing BEFORE the drain begins, so in-flight work finishes
// against a socket nothing new arrives on — and the process is not killed
// while it does it. Drain is ONE-WAY; see its doc comment.
//
// The engine, the per-check budget, the bounded result cache, the HTTP
// handlers and the opt-in lifecycle and sd_notify wiring are concrete and live
// in internal/service/app/health.
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
