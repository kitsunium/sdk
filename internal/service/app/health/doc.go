// Package health — hosts Ask, the client half of a readiness probe: the
// question a container's HEALTHCHECK asks a running process, from an image
// that has no shell and no curl to ask it with (ADR 0131).
//
// Package health — hosts the wire shape of a probe response: the body, and the
// one check inside it. The two live together because the pair IS the document,
// and because keeping them in one file keeps the list of what may leave this
// process on one screen.
//
// Package health — hosts the lifecycle wiring: the one registration that makes
// "not ready" precede a drain instead of following it.
//
// Package health — hosts Config, the registry's construction parameters, and
// the two bounds the SDK will not let a caller past.
//
// Package health — hosts the registered check: what the registry remembers
// about it between probes, and the three accesses that read and move that
// memory.
//
// Package health — hosts the three HTTP handlers. There are three because a
// single handler that read the probe out of the URL would let a routing
// mistake serve liveness on the readiness path — which is the domain's central
// failure wearing a different hat.
//
// Package health — hosts HandlerConfig, the one knob the three handlers take.
//
// Package health implements the SDK's process-health domain: the registry
// behind core/app/health.Health, the per-check budget, the bounded result cache,
// the three HTTP handlers, and the opt-in lifecycle and sd_notify wiring.
// Admitted by ADR 0060.
//
// # What the handler hides
//
// A probe endpoint is exposed more widely than whoever added it expected. The
// body therefore never carries a check's raw error: a handler renders the
// deepest errs Public it can find, and a fixed SDK string when there is none.
// A caller's own typed error keeps its identity through origin-wins, so a
// Public they wrote — already validated wire-safe by errs — is what a stranger
// reads, while `dial tcp 10.0.3.14:5432: connect: connection refused` is
// replaced wholesale rather than trimmed. The full error goes to
// Config.OnReport, which is the caller's own log.
//
// # What a budget expiring means
//
// Exactly three things: the run's context is cancelled (an announcement), the
// registry stops waiting, and a [corehealth.CheckTimeout] result is recorded. No
// goroutine is killed — Go cannot — and nothing the check holds is closed on
// its behalf. A check that outlives its budget keeps ONE goroutine until it
// returns, and the next probe joins that same run instead of starting another;
// see [inflight].
//
// Package health — hosts the single-flight run that keeps a wedged check from
// becoming a goroutine factory.
//
// Package health — hosts the opt-in sd_notify wiring. It WIRES the notifier
// the SDK already ships; it does not reimplement one.
//
// Package health — hosts the probe answers: the phase the process is in, the
// three short-circuits that make the phases mean something, and the
// aggregation rule.
//
// Package health — hosts the execution of one check: its budget, its panic
// recovery, and the wrapping that decides what a stranger reading the probe
// body is allowed to learn.
package health
