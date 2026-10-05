// Package health — ranges 0.2.29.* (ADR 0060 core/app/health block) and
// 0.3.59.* (ADR 0060 service/app/health block, declared here since ADR 0160).
//
// Package health — declares the sentinel *errs.Error outcomes of the domain:
// the registration refusals, and the engine's verdicts on a probe and on the
// loopback Ask. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
//
// Every Public of an engine verdict is written to be READ BY A STRANGER. A
// probe endpoint is routinely exposed more widely than whoever added it
// expected — a mesh sidecar, a load balancer health page, an uptime checker —
// so these strings name a condition and never a cause, a host, a port or a
// query.
//
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
//
// Package health — hosts StartupCheckValue, and the table that says what each
// of the three registrations is allowed to express. They are three types and
// not one with a Probe field, because a field is set by distraction and a type
// is not.
//
// Package health — hosts LivenessCheckValue, the registration a dependency
// cannot be written into. See health_check.go for the table of what each of
// the three may say.
//
// Package health — hosts ReadinessCheckValue, the only registration that can
// express a dependency. See health_check.go for the table of what each of the
// three may say.
//
// Package health — hosts Probe, the three questions an orchestrator asks.
//
// Package health — hosts ResultValue and ReportValue, what a probe answers
// with.
//
// Package health — hosts Status, the verdict a check and a probe both carry,
// and the aggregation rule that turns the first into the second.
package health
