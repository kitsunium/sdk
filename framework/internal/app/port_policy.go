package app

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

// The policies around the calls a port makes (issue #483, part 2):
// [PortService.Guard] puts the SDK's resilience between the caller and what
// the port is bound to, each policy declaring, in the port's service, the
// settings that tune it — <service>-port-<name>-<knob>, the code's value
// their default, the configuration and the variables overriding them as for
// an endpoint's policies (policy.go), a value a policy cannot run with
// stopping the start.
//
// The composition is fixed, whatever the order the policies are given in,
// the outermost first:
//
//	Retry → Hedge → Breaker → Throttle → Timeout → what the port is bound to
//
// It is resilience4j's (Retry ( CircuitBreaker ( RateLimiter ( TimeLimiter (
// Bulkhead ( call ))))), its decorators' aspect order) and Polly's (the
// standard pipeline of Microsoft.Extensions.Http.Resilience: total timeout →
// retry → circuit breaker → attempt timeout; its hedging pipeline: hedging →
// circuit breaker → attempt timeout), for the same reasons:
//
//   - the timeout is the innermost, a deadline per attempt: a slow attempt is
//     a failure the breaker counts and the retry replays — outside the retry
//     it would cut the retries short instead;
//   - the breaker is inside the retry and the hedge, so every attempt and
//     every duplicate is counted, and an open breaker refuses each one at
//     once — its CircuitOpen is not replayed: failing fast is its point;
//   - the throttle is inside the breaker: it judges every request the
//     dependency is sent, as the SRE book's client-side throttling counts
//     them, and its own refusals, made without sending anything, are not
//     the dependency failing — the breaker does not count them;
//   - the hedge is inside the retry: each attempt may race a duplicate, and
//     a retry follows only once every copy failed;
//   - the retry is the outermost, and shares the port's retry budget with
//     the hedge (the SDK's Retry(Hedge(op)) shape), so a dependency that
//     fails for every caller receives fewer extra calls, not more.
//
// A failure is the dependency's when the start would answer it as a 5xx,
// a 408 or a 429 (transient); a refusal of the caller's own — not found,
// invalid, forbidden — is replayed by no retry and opens no breaker, and the
// refusals the policies make themselves (an open breaker, a throttle, an
// exhausted retry or budget, a policy refused) are never replayed by
// another: a port bound to an operation that calls a guarded port of its
// own does not multiply its retries.

// The places of the layers, the outermost first.
const (
	rankRetry int = iota + 1
	rankHedge
	rankBreaker
	rankThrottle
	rankTimeout
)

// The retry budget a port's retry and hedge share when the port gives none:
// gRPC's retryThrottling example, ten tokens and a tenth of one put back by
// a success.
const (
	defaultBudgetTokens int     = 10
	defaultBudgetRatio  float64 = 0.1
)

// portGuards are the policies around a port's calls.
type portGuards struct {
	// layers are the policies, the outermost first.
	layers []portLayer
	// budgets are the Budget options given; the first is the port's.
	budgets []*budgetGuard
	// budget is the retry budget the retry and the hedge share; nil when
	// the port has neither.
	budget *budgetGuard
	// idempotent is the port's declaration that its operation may run more
	// than once for one call ([Idempotent]).
	idempotent bool
}

// portCall is one call through a port's policies.
type portCall struct {
	a  *App
	sp *span
	// budget is the port's retry budget in the app's start, nil without one.
	budget resilience.RetryBudget
}

// portPolicyOption is a [PortPolicyConfigurer].
type portPolicyOption func(g *portGuards)

// portPolicyConfigure sets the option on what it configures.
func (f portPolicyOption) portPolicyConfigure(g *portGuards) { f(g) }

// Guard puts policies around the calls the port makes — [Retry], [Budget],
// [Breaker], [Hedge] with [Idempotent], [Throttle], [Timeout] — and returns
// the port. Whatever the order they are given in, they compose the outermost
// first as Retry, Hedge, Breaker, Throttle, Timeout, around the operation
// the port is bound to; the retry and the hedge share the port's retry
// budget.
//
// Each declares, in the port's service, the settings that tune it, named
// <service>-port-<name>-<knob>: the code's value is the default, which
// config/<env>.* and <PRODUCT>_<NAME> override, and a value a policy cannot
// run with stops the start. A port is guarded once: a second Guard is
// refused.
//
//	var Quote = Service.Port[QuoteRequest, Price]("quote").
//		Guard(app.Timeout(2*time.Second), app.Retry(3, 100*time.Millisecond), app.Breaker(5, 30*time.Second))
func (p *PortService[Req, Resp]) Guard(opts ...PortPolicyConfigurer) *PortService[Req, Resp] {
	if p.guards != nil {
		if p.svc != nil {
			p.svc.problemSaid(p.decl, p.id, say("port.guard-twice", "port", p.id))
		}
		return p
	}
	g := &portGuards{}
	for _, o := range opts {
		if o != nil {
			o.portPolicyConfigure(g)
		}
	}
	g.settle(p.svc, &p.nodeBase)
	p.guards = g
	return p
}

// add adds a layer.
func (g *portGuards) add(l portLayer) { g.layers = append(g.layers, l) }

// settle orders the layers, gives the retry and the hedge their budget, and
// declares every setting in svc — none when the port is no service's.
func (g *portGuards) settle(svc *Service, n *nodeBase) {
	slices.SortStableFunc(g.layers, func(x, y portLayer) int { return cmp.Compare(x.rank(), y.rank()) })
	g.share()
	if svc == nil {
		return
	}
	g.judge(svc, n)
	g.declare(svc, n)
}

// share gives the retry and the hedge their budget: the first Budget given,
// else the default one; none when the port neither retries nor hedges.
func (g *portGuards) share() {
	if !g.has(rankRetry) && !g.has(rankHedge) {
		return
	}
	g.budget = &budgetGuard{tokens: defaultBudgetTokens, ratio: defaultBudgetRatio}
	if len(g.budgets) > 0 {
		g.budget = g.budgets[0]
	}
}

// judge refuses what the port's policies are given wrong: a budget no retry
// nor hedge shares, a hedge on a port not said idempotent.
func (g *portGuards) judge(svc *Service, n *nodeBase) {
	if len(g.budgets) > 0 && g.budget == nil {
		svc.problemSaid(n.decl, n.id, say("policy.budget-alone", "port", n.id))
	}
	if g.has(rankHedge) && !g.idempotent {
		svc.problemSaid(n.decl, n.id, say("policy.hedge-idempotent", "port", n.id))
	}
}

// declare declares the settings of the port's budget and policies.
func (g *portGuards) declare(svc *Service, n *nodeBase) {
	if g.budget != nil {
		// A second Budget would declare the first one's settings again:
		// the declaration says so (policy.setting-twice).
		g.budget.declare(svc, n)
		for _, b := range g.budgets[min(1, len(g.budgets)):] {
			b.declare(svc, n)
		}
	}
	for _, l := range g.layers {
		l.declare(svc, n)
	}
}

// has reports whether a layer of rank r guards the port.
func (g *portGuards) has(r int) bool {
	return slices.ContainsFunc(g.layers, func(l portLayer) bool { return l.rank() == r })
}

// drawn are the port's policies as the graph draws them, the outermost
// first, its budget before them.
func (g *portGuards) drawn() []model.Mechanic {
	if g == nil {
		return nil
	}
	var out []model.Mechanic
	if g.budget != nil {
		out = append(out, g.budget.mechanic())
	}
	for _, l := range g.layers {
		out = append(out, l.mechanic())
	}
	return out
}

// run runs attempt through the policies, in the app of c.
func (g *portGuards) run(ctx context.Context, c *portCall, attempt func(context.Context) error) error {
	if g.budget != nil {
		c.budget = g.budget.of(c.a)
	}
	run := attempt
	for _, l := range slices.Backward(g.layers) {
		r, inner := l.runner(c), run
		run = func(ctx context.Context) error { return r.Run(ctx, inner) }
	}
	return run(ctx)
}

// guarded runs the call through the port's policies: each attempt — a
// retry's, a hedge's duplicate — runs what the port is bound to, and a
// success answers. An attempt's answer is kept as the call's only while the
// attempt's context lives — the last such one wins —: an answer that came
// after its timeout is the failure the timeout makes of it, and a later
// attempt's answer replaces it. One that came late is the call's only when
// no other came, the chain having said success all the same.
func (p *PortService[Req, Resp]) guarded(ctx context.Context, a *App, sp *span, req Req) (Resp, error) {
	var answers portAnswers[Resp]
	err := p.guards.run(ctx, &portCall{a: a, sp: sp}, func(ctx context.Context) error {
		r, err := p.run(ctx, a, req)
		if err == nil {
			answers.keep(r, ctx.Err() == nil)
		}
		return err
	})
	if err != nil {
		var zero Resp
		return zero, err
	}
	return answers.answer(), nil
}

// portAnswers are the answers of a call's attempts, which may run at once.
type portAnswers[Resp any] struct {
	mu    sync.Mutex
	fresh Resp
	late  Resp
	held  bool
}

// keep keeps r, an attempt's answer: as fresh while its context lived.
func (w *portAnswers[Resp]) keep(r Resp, live bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if live {
		w.fresh, w.held = r, true
		return
	}
	w.late = r
}

// answer is the call's answer: the last fresh one, else the late one.
func (w *portAnswers[Resp]) answer() Resp {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.held {
		return w.fresh
	}
	return w.late
}

// retried is what a call says of a retry about to wait: on the port's span,
// a retry event and the count of its retries; on the telemetry port, a
// retry event.
func (c *portCall) retried(attempt int, err error, delay time.Duration) {
	_, body := describe(err)
	c.sp.attr("retries", strconv.Itoa(attempt))
	if c.sp.sdk != nil {
		c.sp.sdk.AddEvent("retry", trace.Int64("attempt", int64(attempt)), trace.String("delay", delay.String()), trace.String("code", body.Code))
	}
	c.a.reportRetry(c.sp, err, delay)
}

// transient reports whether err is the dependency failing — what the start
// answers as a 5xx, a 408 or a 429 —, which a retry replays, a breaker and
// a throttle count. A refusal of the caller's own is not, nor a caller that
// left, nor the refusals the policies make themselves: an open breaker, a
// throttle, an exhausted retry or budget, a policy refused.
func transient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || policyRefusal(err) {
		return false
	}
	status, _ := describe(err)
	return status >= http.StatusInternalServerError || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests
}

// policyRefusal reports whether err is a refusal a policy made itself.
func policyRefusal(err error) bool {
	return errors.Is(err, resilience.CircuitOpen) || errors.Is(err, resilience.Throttled) ||
		errors.Is(err, resilience.RetryExhausted) || errors.Is(err, resilience.RetryBudgetExceeded) ||
		errors.Is(err, resilience.PolicyMisconfigured)
}

// portPolicyProblems are what stops a start in the app's guarded ports: a
// policy the SDK refuses with the values the start resolved.
func (a *App) portPolicyProblems() []model.Diagnostic {
	var out []model.Diagnostic
	for _, gp := range a.guardedPorts() {
		for _, l := range gp.guards.layers {
			if p := l.started(a, gp.id); !p.empty() {
				out = append(out, diagnosticOf("error", gp.id, a.source(&gp.decl), p))
			}
		}
	}
	return out
}

// guardedPort is a port that guards its calls, whatever its types.
type guardedPort struct {
	id     string
	decl   pos
	svc    *Service
	name   string
	guards *portGuards
}

// guardedPorts are the ports of the app's services that guard their calls.
func (a *App) guardedPorts() []guardedPort {
	var out []guardedPort
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if g, ok := n.(interface{ portGuards() *portGuards }); ok && g.portGuards() != nil {
				b := n.base()
				out = append(out, guardedPort{id: b.id, decl: b.decl, svc: svc, name: b.name, guards: g.portGuards()})
			}
		}
	}
	return out
}

// portGuards are the port's policies, nil without any.
func (p *PortService[Req, Resp]) portGuards() *portGuards { return p.guards }

// perStart is a value built once per start of the app that reads it, from
// the settings that start resolved, on its clock: a restart builds it
// again.
type perStart[T any] struct {
	built atomic.Pointer[startValue[T]]
}

// startValue is a perStart's value, and the settings of the start it was
// built for.
type startValue[T any] struct {
	values *map[string]any
	v      T
}

// get is the value of app a's start, built now when there is none — once:
// a goroutine that loses the race takes the winner's.
func (s *perStart[T]) get(a *App, build func(clk clock.Timed) T) T {
	values, clk := startOf(a)
	for {
		b := s.built.Load()
		if b != nil && b.values == values {
			return b.v
		}
		next := &startValue[T]{values: values, v: build(clk)}
		if s.built.CompareAndSwap(b, next) {
			return next.v
		}
	}
}

// policyLayer is a policy of part 1 put around a port's calls: the
// timeout and the throttle, whose runner needs nothing but the clock.
type policyLayer struct {
	p     policy
	place int
}

// rank is the layer's place.
func (l *policyLayer) rank() int { return l.place }

// declare declares the policy's settings.
func (l *policyLayer) declare(svc *Service, n *nodeBase) { l.p.guard.declare(svc, n) }

// mechanic is the policy's step.
func (l *policyLayer) mechanic() model.Mechanic { return l.p.guard.mechanic() }

// runner is the policy's runner in the call's app.
func (l *policyLayer) runner(c *portCall) resilience.Runner { return l.p.runnerFor(c.a) }

// started has nothing to ready.
func (*policyLayer) started(_ *App, _ string) phrase { return phrase{} }

// timeoutOption is [Timeout]: a deadline on an operation's handler, or on
// each attempt of a port's call.
type timeoutOption time.Duration

// guard is a fresh timeout guard: each operation declares its own setting.
func (d timeoutOption) guard() *timeoutGuard { return &timeoutGuard{after: time.Duration(d)} }

// call adds the timeout to an operation's policies.
func (d timeoutOption) call(o *callOptions) {
	o.policies = append(o.policies, &policy{guard: d.guard()})
}

// endpointConfigure sets the option on what it configures.
func (d timeoutOption) endpointConfigure(o *endpointOptions) { d.call(&o.callOptions) }

// exposeConfigure sets the option on what it configures.
func (d timeoutOption) exposeConfigure(o *endpointOptions) { d.call(&o.callOptions) }

// commandConfigure sets the option on what it configures.
func (d timeoutOption) commandConfigure(o *commandOptions) { d.call(&o.callOptions) }

// queryConfigure sets the option on what it configures.
func (d timeoutOption) queryConfigure(o *queryOptions) { d.call(&o.callOptions) }

// portPolicyConfigure sets the option on what it configures.
func (d timeoutOption) portPolicyConfigure(g *portGuards) {
	g.add(&policyLayer{p: policy{guard: d.guard()}, place: rankTimeout})
}

// knobIn is a port policy's setting as app a's start resolved it — its
// default before, or outside an app —, or the code's value when the port
// declared no setting. A port's runners are built from it rather than from
// the setting's Get, which reads the app the service is mounted in: the
// start judges a breaker before it mounts the services.
func knobIn[T SettingValue](a *App, st *SettingService[T], def T) T {
	switch {
	case st == nil:
		return def
	case a == nil:
		return st.def
	}
	return resolvedSetting(a, st)
}
