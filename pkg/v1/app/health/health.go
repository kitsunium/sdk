//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/app/health .

// Package health is the public facade for the SDK's process-health domain:
// the three probes an orchestrator asks, and the checks that answer them.
//
//	h := health.New(health.Config{})
//
//	// a dependency. It can only be registered here.
//	_ = h.AddReadiness(health.ReadinessCheck{Name: "db", Check: db.PingContext})
//
//	// process-local evidence. It takes no context, so a dependency cannot
//	// reach it: `health.LivenessCheck{Check: db.PingContext}` does not compile.
//	_ = h.AddLiveness(health.LivenessCheck{Name: "workers", Check: pool.Alive})
//
//	mux.Handle("/healthz", health.NewLivenessHandler(h, health.HandlerConfig{}))
//	mux.Handle("/readyz", health.NewReadinessHandler(h, health.HandlerConfig{}))
//
// # Liveness and readiness are not the same signal
//
// A saturated service is not a dead service. When a liveness probe fails
// because a dependency is slow, the orchestrator KILLS the replica; its
// traffic moves to the replicas that remain, which are now nearer the same
// saturation, and fail the same probe. That is a cascading outage whose cause
// is the probe, and it is the reason this package exists.
//
// So the rule is structural, not documentary. A dependency check cannot be
// registered on liveness:
//
//   - [ReadinessCheck] and [StartupCheck] carry a [Check] — func(ctx) error —
//     which is the shape every dependency API in Go wants.
//   - [LivenessCheck] carries a [SelfCheck] — func() error — with no context
//     to hand a dependency call at all. The two function types are not
//     assignable in either direction, so the mistake does not compile.
//
// Two further absences say the same thing: [LivenessCheck] has no NonCritical
// field, because "somewhat irrecoverable" restarts nothing; and no MaxAge,
// because a cached liveness answer means a dead process can keep replaying the
// "alive" it recorded before it died.
//
// # The three states
//
// Startup is not a third flavour of readiness. It is the state in which the
// process must be neither killed nor routed to:
//
//   - While a [StartupCheck] has yet to pass, the liveness probe reports
//     healthy WITHOUT running anything, and readiness reports not-ready
//     without running anything. A slow start therefore cannot become a restart
//     loop. The price is stated: a process wedged during startup is never
//     killed by liveness, and the startup probe's own failure is the signal an
//     orchestrator must act on.
//   - A startup check runs until it passes ONCE and is then never run again.
//   - After [Health].Drain, readiness reports not-ready permanently while
//     liveness keeps answering — so the replica is withdrawn from routing
//     rather than killed while it finishes its work.
//
// # What a timeout means
//
// Every check has a budget, and a check that has not answered by then is a
// FAILURE, not an unknown. A probe exists to answer within a bounded time, and
// "I could not answer" is operationally the same as "no" for whoever must
// decide about routing. What is preserved is the distinction: the result
// carries TimedOut, which separates "the dependency said no" from "the
// dependency said nothing".
//
// A non-positive [StartupCheck].Timeout clamps to [Config].DefaultTimeout, and
// a non-positive one there clamps to [DefaultCheckTimeout] (1s). Zero is never
// read as "no time at all": that would turn a forgotten line into a probe
// where every check fails before it runs.
//
// The budget bounds the WAIT. Nothing kills a check's goroutine — Go cannot —
// and nothing it holds is closed on its behalf. A check that outlives its
// budget keeps exactly ONE goroutine until it returns, because the next probe
// joins that same run instead of starting a second: an endpoint polled every
// ten seconds against a dependency call with no deadline must not accumulate a
// goroutine every ten seconds.
//
// # What a cached answer means
//
// [ReadinessCheck].MaxAge replays the last SUCCESS for up to that long, so an
// expensive check is not paid for on every poll. Three bounds keep it from
// becoming a lie:
//
//   - It is OFF by default. A probe that silently replays a measurement nobody
//     asked it to keep is the more surprising of the two behaviours.
//   - A value above [MaxCacheAge] (30s) is REFUSED at registration, never
//     clamped. Serving 30s to a caller who asked for five minutes is the same
//     lie in a smaller size.
//   - Only successes are cached. A failure is re-measured on every probe,
//     because the answer an operator needs promptly is the one that says the
//     outage ended.
//
// Every result carries the instant it was measured, so a replayed answer is a
// dated statement rather than a current one.
//
// # Aggregation
//
// A probe is as healthy as its least healthy check, and criticality decides
// how bad a failure is. A failing check is [StatusUnhealthy], or
// [StatusDegraded] when it was registered NonCritical. Degraded SERVES — the
// handler returns 200 — because a degraded replica removed from rotation would
// make "non-critical" mean nothing. Degraded never masks unhealthy.
//
// The field is spelled NonCritical rather than Critical so that its zero value
// is the strict reading: a check somebody forgot to mark must not be one that
// can never take a replica out of rotation.
//
// A registry with NO checks is legitimate and answers healthy. The process
// replying is the evidence.
//
// # What the handler exposes, and what it hides
//
// The body is `{"status":"healthy"}` and nothing else unless
// [HandlerConfig].Detail is set. Even then, a check contributes its name, its
// status, its timing and a reason drawn from the errs Public half — never a
// raw error, never a Private, never a field. A caller's own typed error keeps
// its identity through origin-wins, so a wire-safe message they wrote is what
// a stranger reads; a plain error such as `dial tcp 10.0.3.14:5432: connect:
// connection refused` is replaced wholesale rather than trimmed. The full
// error goes to [Config].OnReport, which is the caller's own log.
//
// The handlers write to the ResponseWriter and nowhere else — never stdout,
// which may be the process's protocol channel (ADR 0030). Responses carry
// Cache-Control: no-store, only GET and HEAD are answered, and the query
// string is never read.
//
// There are three handler constructors rather than one that reads the probe
// from the URL, because a routing mistake must not be able to serve liveness
// on the readiness path.
//
// # Wiring it to a lifecycle
//
// [Component] adapts a registry to pkg/v1/app/lifecycle. Add it LAST: added last
// it starts last, so its startup checks run after everything they depend on is
// up; and added last it stops FIRST, so readiness flips to not-ready before a
// single component begins closing. That ordering is what lets an orchestrator
// withdraw the replica from routing before the drain begins, instead of
// draining against traffic that keeps arriving.
//
// # Asking a running process
//
// [Ask] is the other end: the question a container's HEALTHCHECK asks, from a
// binary that ships in an image with no shell and no curl to ask it with. The
// same executable answers it as a subcommand:
//
//	status, err := health.Ask(ctx, health.AskConfig{Addr: ":4000", Path: "/readyz"})
//	if err != nil {
//		fmt.Fprintln(os.Stderr, err) // why: ASK_UNREACHABLE, ASK_TIMEOUT, ASK_NOT_READY
//		os.Exit(1)
//	}
//
// Addr is the address the process LISTENS on, spelled as its listener was
// given it; a host that names no particular address — empty, 0.0.0.0, :: — is
// dialled on this machine's loopback of the same family, since no connection
// can be made to an unspecified address. Ready means one thing: the process
// answered 200. The exchange is bounded by [DefaultAskTimeout] (or
// [AskConfig].Timeout) and by the caller's context, follows no redirect, goes
// through no proxy whatever the environment says, reads at most
// [MaxAskDrainBytes] of the body and closes it, and no byte of that body ever
// reaches an error: [AskNotReady] carries the status alone. The body is part of
// the answer: a process that sends 200 and then stalls until the bound ends
// has not answered within it, and gets [AskTimeout].
package health
