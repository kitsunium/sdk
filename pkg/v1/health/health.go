//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/health .

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
// [Component] adapts a registry to pkg/v1/lifecycle. Add it LAST: added last
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

import (
	"context"
	"net/http"
	"time"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
	svchealth "github.com/kitsunium/sdk/internal/service/health"
)

const (
	// StatusUnhealthy is a failing critical check, or a probe with one. It is
	// the ZERO value of Status on purpose: a forgotten assignment must read
	// as "not serving", never as "everything is fine".
	StatusUnhealthy Status = corehealth.StatusUnhealthy
	// StatusDegraded is a failing NON-critical check. It is a serving state:
	// the handler returns 200.
	StatusDegraded Status = corehealth.StatusDegraded
	// StatusHealthy is every check passing — including a probe with no checks
	// registered at all.
	StatusHealthy Status = corehealth.StatusHealthy
	// ProbeStartup asks "is this process still coming up?".
	ProbeStartup Probe = corehealth.ProbeStartup
	// ProbeReadiness asks "can this replica take traffic right now?".
	ProbeReadiness Probe = corehealth.ProbeReadiness
	// ProbeLiveness asks "is this process irrecoverable?".
	ProbeLiveness Probe = corehealth.ProbeLiveness
	// DefaultCheckTimeout is the per-check budget a non-positive Timeout
	// clamps to.
	DefaultCheckTimeout time.Duration = svchealth.DefaultCheckTimeout
	// MaxCacheAge is the ceiling on ReadinessCheck.MaxAge. Above it, a
	// registration is refused rather than clamped.
	MaxCacheAge time.Duration = svchealth.MaxCacheAge
	// DefaultAskTimeout is the budget an Ask gets when AskConfig.Timeout is
	// zero: three seconds, an order of magnitude under Docker's own
	// HEALTHCHECK timeout, so a probe that gets no answer says why first.
	DefaultAskTimeout time.Duration = svchealth.DefaultAskTimeout
	// MaxAskDrainBytes is how much of a response body Ask reads before it
	// closes the connection: enough for any readiness answer to end politely,
	// and a bound on one that never ends.
	MaxAskDrainBytes int64 = svchealth.MaxAskDrainBytes
)

// Check is the public alias for the ctx-aware body of a startup or readiness
// check. It is the shape a dependency call has.
type Check = corehealth.Check

// SelfCheck is the public alias for the body of a LIVENESS check. It takes no
// context, which is what keeps a dependency call out of the probe that
// restarts the process.
type SelfCheck = corehealth.SelfCheck

// Health is the public alias for the registry contract.
type Health = corehealth.Health

// Status is the public alias for a check's or a probe's verdict.
type Status = corehealth.Status

// Probe is the public alias for which of the three questions is being asked.
type Probe = corehealth.Probe

// StartupCheck is the public alias for a check that gates startup.
type StartupCheck = corehealth.StartupCheckValue

// ReadinessCheck is the public alias for a check that gates routing. Every
// dependency check belongs here.
type ReadinessCheck = corehealth.ReadinessCheckValue

// LivenessCheck is the public alias for process-local evidence that the
// process is not irrecoverable.
type LivenessCheck = corehealth.LivenessCheckValue

// Result is the public alias for one check's answer.
type Result = corehealth.ResultValue

// Report is the public alias for one probe's whole answer.
type Report = corehealth.ReportValue

// Config is the public alias for the registry's construction parameters.
type Config = svchealth.Config

// HandlerConfig is the public alias for a handler's body verbosity.
type HandlerConfig = svchealth.HandlerConfig

// AskConfig is the public alias for where a process listens and which path
// answers whether it is ready: Addr and Path are required, Timeout and Clock
// have working zeros.
type AskConfig = svchealth.AskConfig

var (
	// InvalidCheck is returned by every Add for a check that could never run
	// — an empty Name or a nil Check. The "missing" field names which.
	InvalidCheck = corehealth.InvalidCheck
	// DuplicateCheck is returned by an Add whose name is already registered
	// on that probe. The namespace is per probe, so the same name may appear
	// once on readiness and once on liveness.
	DuplicateCheck = corehealth.DuplicateCheck
	// UnknownProbe is carried by a report for a Probe value the SDK never
	// mints, including the zero value.
	UnknownProbe = corehealth.UnknownProbe
	// CheckPanicked is the failure of a check whose body panicked. It was
	// recovered; the panic value travels as a field and never reaches a body.
	CheckPanicked = corehealth.CheckPanicked
	// CheckFailed is the identity a PLAIN error from a check is given. An
	// error that already carries an SDK Code keeps its own.
	CheckFailed = svchealth.CheckFailed
	// CheckTimeout reports a check that had not answered when its budget
	// expired. A failure, not an unknown — Result.TimedOut is what
	// distinguishes it.
	CheckTimeout = svchealth.CheckTimeout
	// StaleCacheWindow is returned by AddReadiness for a MaxAge above
	// MaxCacheAge.
	StaleCacheWindow = svchealth.StaleCacheWindow
	// StartupPending is the synthetic result of a readiness probe answered
	// while the process is still starting. No readiness check was run.
	StartupPending = svchealth.StartupPending
	// Draining is the synthetic result of a readiness probe answered after
	// Drain. No readiness check was run, and no later probe will say anything
	// else.
	Draining = svchealth.Draining
	// NotifyFailed reaches Config.OnNotifyError when an opt-in sd_notify
	// datagram could not be delivered.
	NotifyFailed = svchealth.NotifyFailed
	// AskMisconfigured refuses an Ask no answer could satisfy — an address
	// with no usable port, a path that is not absolute, a negative timeout —
	// before anything is dialled. The "argument" field names which.
	AskMisconfigured = svchealth.AskMisconfigured
	// AskUnreachable reports an Ask the process never answered because the
	// connection or the request failed; the transport's error is the cause.
	AskUnreachable = svchealth.AskUnreachable
	// AskTimeout reports an Ask with no answer when its budget, or the
	// caller's context, ended. A caller's own context error stays in the
	// chain, so errors.Is answers context.Canceled or DeadlineExceeded too.
	AskTimeout = svchealth.AskTimeout
	// AskNotReady reports an Ask the process answered with a status other
	// than 200 — a redirect included, since none is followed. The "status"
	// field carries it; the body never does.
	AskNotReady = svchealth.AskNotReady
)

// New returns a Health. It cannot fail: a nil cfg.Clock falls back to the wall
// clock, a non-positive cfg.DefaultTimeout to DefaultCheckTimeout, and nil
// hooks to no observation. What can fail fails at registration.
func New(cfg Config) Health {
	//: delegate to the service constructor.
	return svchealth.New(cfg)
}

// Worst returns the more severe of two statuses — the SDK's whole aggregation
// rule, exported so a caller composing several registries applies the same one.
func Worst(a, b Status) Status {
	//: delegate to the domain rule.
	return corehealth.Worst(a, b)
}

// Component adapts a registry to pkg/v1/lifecycle: its Start runs the startup
// checks, its Stop marks the process draining. The returned value IS a
// lifecycle.Component.
//
// Add it LAST — see the package documentation for why one rule covers both
// directions.
func Component(registry Health, name string) corelc.ComponentValue {
	//: delegate to the service helper.
	return svchealth.Component(registry, name)
}

// NewStartupHandler serves the startup probe: 200 once every startup check has
// passed, 503 while any has not.
func NewStartupHandler(registry Health, cfg HandlerConfig) http.Handler {
	//: delegate to the service handler.
	return svchealth.NewStartupHandler(registry, cfg)
}

// NewReadinessHandler serves the readiness probe: 200 while the replica can
// take traffic (healthy OR degraded), 503 otherwise — including while starting
// and after Drain.
func NewReadinessHandler(registry Health, cfg HandlerConfig) http.Handler {
	//: delegate to the service handler.
	return svchealth.NewReadinessHandler(registry, cfg)
}

// NewLivenessHandler serves the liveness probe: 200 unless the process is
// irrecoverable. Nothing outside the process can make it return 503, because
// nothing outside the process can be registered on it.
func NewLivenessHandler(registry Health, cfg HandlerConfig) http.Handler {
	//: delegate to the service handler.
	return svchealth.NewLivenessHandler(registry, cfg)
}

// Ask asks the process listening on cfg.Addr whether it is ready: one GET of
// cfg.Path over plain HTTP, the question a container's HEALTHCHECK asks. It
// returns the status the process answered — zero when no whole answer, body
// included, arrived within the bound — and a nil error exactly when that
// status is 200; otherwise the error is
// AskMisconfigured, AskUnreachable, AskTimeout or AskNotReady. See the package
// documentation for what the exchange refuses to do.
func Ask(ctx context.Context, cfg AskConfig) (status int, err error) {
	//: delegate to the service probe.
	return svchealth.Ask(ctx, cfg)
}
