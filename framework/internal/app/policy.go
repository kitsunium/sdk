package app

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The policies in front of an operation's handler (issue #483): [RateLimit],
// [RateLimitPerClient], [Timeout] and [Bulkhead] put the SDK's resilience
// around an endpoint, a command or a query, and each declares, in the
// service of the operation it guards, the settings that tune it. The value
// given in code is the setting's default; config/config.<ext>,
// config/<env>.<ext>, <PRODUCT>_<NAME> and app.Set override it as they
// override any setting, runtime.config showing where the value came from
// (ADR 0097), and a key no service declares stays refused.
//
// A policy's setting is named <service>-<operation>-<knob>, the service's and
// the operation's names in lower case, a capital starting a word of its own
// and any other character a setting's name cannot hold a dash
// (policySettingName) — "shop-create-item-rate", SHOP_SHOP_CREATE_ITEM_RATE —
// and qualified with its module's name for a module's service's:
//
//	RateLimit           <op>-rate, <op>-burst, <op>-rate-wait
//	RateLimitPerClient  <op>-client-rate, <op>-client-burst
//	Timeout             <op>-timeout
//	Bulkhead            <op>-bulkhead, <op>-bulkhead-queue, <op>-bulkhead-wait
//
// The start refuses a value a policy cannot run with — a rate that is not a
// positive number, a burst or a bulkhead below one, a timeout that is not
// positive, a negative wait or queue — the code's default included, where the
// SDK would have refused every call (ADR 0031). An operation takes one policy
// of a kind: a second one, or two operations of a service whose names spell
// alike, would share a setting, and the declaration says so
// (policy.setting-twice).
//
// The SDK's runner is built from the values the start resolved, on the app's
// clock, the first time the operation runs in that start (policy.runnerFor):
// a restart begins with full buckets and empty bulkheads.

// The knobs of the policies, each a suffix of its setting's name.
const (
	knobRate          = "rate"
	knobBurst         = "burst"
	knobRateWait      = "rate-wait"
	knobClientRate    = "client-rate"
	knobClientBurst   = "client-burst"
	knobTimeout       = "timeout"
	knobBulkhead      = "bulkhead"
	knobBulkheadQueue = "bulkhead-queue"
	knobBulkheadWait  = "bulkhead-wait"
)

// resiliencePackage is the SDK package every policy runs.
const resiliencePackage = "github.com/kitsunium/sdk/pkg/v1/app/resilience"

// policy is a mechanic an operation runs in front of its handler: what it
// guards with, and the runner it built for the start it last ran in.
type policy struct {
	guard policyGuard
	built atomic.Pointer[builtRunner]
}

// builtRunner is a policy's runner, and the settings of the start it was
// built for.
type builtRunner struct {
	values *map[string]any
	runner resilience.Runner
}

// run runs op behind the policy, in app a.
func (p *policy) run(ctx context.Context, a *App, op func(context.Context) error) error {
	return p.runnerFor(a).Run(ctx, op)
}

// runnerFor is the policy's runner in app a: the one built for the settings
// its start resolved, built now when there is none — once: a goroutine that
// loses the race takes the winner's.
func (p *policy) runnerFor(a *App) resilience.Runner {
	values, clk := startOf(a)
	for {
		b := p.built.Load()
		if b != nil && b.values == values {
			return b.runner
		}
		next := &builtRunner{values: values, runner: p.guard.runner(clk)}
		if p.built.CompareAndSwap(b, next) {
			return next.runner
		}
	}
}

// startOf is what a policy's runner is built from in app a: the settings
// its start resolved, and its clock — none and the system's outside an app.
func startOf(a *App) (*map[string]any, clock.Timed) {
	if a == nil {
		return nil, clock.System
	}
	clk := a.clock
	if clk == nil {
		clk = clock.System
	}
	return a.settingValues.Load(), clk
}

// declarePolicies declares the settings of an operation's policies in svc.
func declarePolicies(svc *Service, n *nodeBase, policies []*policy) {
	for _, p := range policies {
		p.guard.declare(svc, n)
	}
}

// mechanicsOf are the policies' steps, as the graph draws them.
func mechanicsOf(policies []*policy) []model.Mechanic {
	out := make([]model.Mechanic, len(policies))
	for i, p := range policies {
		out[i] = p.guard.mechanic()
	}
	return out
}

// policyOption is the OperationOption that adds guard's policy.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func policyOption(guard func() policyGuard) OperationOption {
	return callOption(func(o *callOptions) {
		o.policies = append(o.policies, &policy{guard: guard()})
	})
}

// waitOption is [MaxWait].
type waitOption time.Duration

// rateLimitConfigure sets the option on what it configures.
func (w waitOption) rateLimitConfigure(o *rateLimitGuard) { o.wait = time.Duration(w) }

// bulkheadConfigure sets the option on what it configures.
func (w waitOption) bulkheadConfigure(o *bulkheadGuard) { o.wait = time.Duration(w) }

// bulkheadOption configures a bulkhead.
type bulkheadOption func(o *bulkheadGuard)

// bulkheadConfigure sets the option on what it configures.
func (f bulkheadOption) bulkheadConfigure(o *bulkheadGuard) { f(o) }

// MaxWait lets a call wait, at most d on the app's clock, instead of being
// refused at once: given to [RateLimit], for a token due within d — the
// calls after it queue behind it —; given to [Bulkhead], for a slot to free,
// in the queue [BulkheadQueue] sizes. A call still refused answers 429 or
// 503 as before; a caller that leaves while it waits leaves the queue. Its
// default is no wait. It is the default of the operation's setting
// <service>-<operation>-rate-wait or -bulkhead-wait, which config/<env>.* and
// <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its methods are unexported — so a
// caller only hands it to the declaration it configures.
func MaxWait(d time.Duration) WaitConfigurer { return waitOption(d) }

// BulkheadQueue lets up to n calls beyond those a [Bulkhead] runs wait for a
// slot, each for its [MaxWait]: without a wait, or with n at 0, the overflow
// is refused at once. A call that finds the queue full answers 503. It is
// the default of the operation's setting <service>-<operation>-bulkhead-queue,
// which config/<env>.* and <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func BulkheadQueue(n int) BulkheadConfigurer {
	return bulkheadOption(func(o *bulkheadGuard) { o.queue = n })
}

// rateLimitGuard is a token bucket shared by every caller ([RateLimit]).
type rateLimitGuard struct {
	rate     float64
	burst    int
	wait     time.Duration
	rateFor  *SettingService[float64]
	burstFor *SettingService[int]
	waitFor  *SettingService[time.Duration]
}

// newRateLimitGuard is [RateLimit]'s policy.
func newRateLimitGuard(perSecond float64, burst int, opts []RateLimitConfigurer) *rateLimitGuard {
	g := &rateLimitGuard{rate: perSecond, burst: burst}
	for _, o := range opts {
		if o != nil {
			o.rateLimitConfigure(g)
		}
	}
	return g
}

// declare declares <op>-rate, <op>-burst and <op>-rate-wait.
func (g *rateLimitGuard) declare(svc *Service, n *nodeBase) {
	g.rateFor = declarePolicySetting(svc, n, knobRate, g.rate, checkRate())
	g.burstFor = declarePolicySetting(svc, n, knobBurst, g.burst, checkAtLeastOne())
	g.waitFor = declarePolicySetting(svc, n, knobRateWait, g.wait, checkWait())
}

// mechanic draws the bucket.
func (g *rateLimitGuard) mechanic() model.Mechanic {
	rate, burst := formatRate(knobValue(g.rateFor, g.rate)), knobValue(g.burstFor, g.burst)
	config := map[string]string{"rate": rate, "burst": strconv.Itoa(burst)}
	label := fmt.Sprintf("%s/s burst %d", rate, burst)
	if wait := knobValue(g.waitFor, g.wait); wait > 0 {
		config["maxWait"] = wait.String()
		label += " wait " + wait.String()
	}
	return model.Mechanic{Kind: "ratelimit", Label: label, Package: resiliencePackage, Config: config}
}

// runner is the SDK's rate limiter.
func (g *rateLimitGuard) runner(clk clock.Timed) resilience.Runner {
	return resilience.NewRateLimiter(resilience.RateLimiterConfig{
		Rate: knobValue(g.rateFor, g.rate), Burst: knobValue(g.burstFor, g.burst),
		MaxWait: knobValue(g.waitFor, g.wait), Clock: clk,
	})
}

// clientLimitGuard is a token bucket per client ([RateLimitPerClient]).
type clientLimitGuard struct {
	rate     float64
	burst    int
	rateFor  *SettingService[float64]
	burstFor *SettingService[int]
}

// declare declares <op>-client-rate and <op>-client-burst.
func (g *clientLimitGuard) declare(svc *Service, n *nodeBase) {
	g.rateFor = declarePolicySetting(svc, n, knobClientRate, g.rate, checkRate())
	g.burstFor = declarePolicySetting(svc, n, knobClientBurst, g.burst, checkAtLeastOne())
}

// mechanic draws the buckets.
func (g *clientLimitGuard) mechanic() model.Mechanic {
	rate, burst := formatRate(knobValue(g.rateFor, g.rate)), knobValue(g.burstFor, g.burst)
	return model.Mechanic{
		Kind: "ratelimit", Label: fmt.Sprintf("%s/s burst %d per client", rate, burst), Package: resiliencePackage,
		Config: map[string]string{"rate": rate, "burst": strconv.Itoa(burst), "per": "client"},
	}
}

// runner is the SDK's keyed rate limiter.
func (g *clientLimitGuard) runner(clk clock.Timed) resilience.Runner {
	return newClientLimiter(knobValue(g.rateFor, g.rate), knobValue(g.burstFor, g.burst), maxClientBuckets, clientBucketIdle, clk)
}

// timeoutGuard is a deadline on the handler ([Timeout]).
type timeoutGuard struct {
	after    time.Duration
	afterFor *SettingService[time.Duration]
}

// declare declares <op>-timeout.
func (g *timeoutGuard) declare(svc *Service, n *nodeBase) {
	g.afterFor = declarePolicySetting(svc, n, knobTimeout, g.after, checkPositive())
}

// mechanic draws the deadline.
func (g *timeoutGuard) mechanic() model.Mechanic {
	after := knobValue(g.afterFor, g.after)
	return model.Mechanic{Kind: "timeout", Label: after.String(), Package: resiliencePackage, Config: map[string]string{"after": after.String()}}
}

// runner is the SDK's timeout.
func (g *timeoutGuard) runner(clk clock.Timed) resilience.Runner {
	return resilience.NewTimeoutWith(resilience.TimeoutConfig{Duration: knobValue(g.afterFor, g.after), Clock: clk})
}

// bulkheadGuard caps the calls run at once ([Bulkhead]).
type bulkheadGuard struct {
	limit    int
	queue    int
	wait     time.Duration
	limitFor *SettingService[int]
	queueFor *SettingService[int]
	waitFor  *SettingService[time.Duration]
}

// newBulkheadGuard is [Bulkhead]'s policy.
func newBulkheadGuard(maxConcurrent int, opts []BulkheadConfigurer) *bulkheadGuard {
	g := &bulkheadGuard{limit: maxConcurrent}
	for _, o := range opts {
		if o != nil {
			o.bulkheadConfigure(g)
		}
	}
	return g
}

// declare declares <op>-bulkhead, <op>-bulkhead-queue and <op>-bulkhead-wait.
func (g *bulkheadGuard) declare(svc *Service, n *nodeBase) {
	g.limitFor = declarePolicySetting(svc, n, knobBulkhead, g.limit, checkAtLeastOne())
	g.queueFor = declarePolicySetting(svc, n, knobBulkheadQueue, g.queue, checkNotNegative())
	g.waitFor = declarePolicySetting(svc, n, knobBulkheadWait, g.wait, checkWait())
}

// mechanic draws the bulkhead.
func (g *bulkheadGuard) mechanic() model.Mechanic {
	limit := knobValue(g.limitFor, g.limit)
	config := map[string]string{"max": strconv.Itoa(limit)}
	label := fmt.Sprintf("%d at once", limit)
	queue, wait := knobValue(g.queueFor, g.queue), knobValue(g.waitFor, g.wait)
	if queue > 0 && wait > 0 {
		config["queue"], config["maxWait"] = strconv.Itoa(queue), wait.String()
		label += fmt.Sprintf(", %d waiting %s", queue, wait)
	}
	return model.Mechanic{Kind: "bulkhead", Label: label, Package: resiliencePackage, Config: config}
}

// runner is the SDK's bulkhead: the one that queues, which is the one that
// refuses at once when nothing may wait.
func (g *bulkheadGuard) runner(clk clock.Timed) resilience.Runner {
	return resilience.NewBulkheadWait(resilience.BulkheadWaitConfig{
		Max: knobValue(g.limitFor, g.limit), Queue: knobValue(g.queueFor, g.queue),
		MaxWait: knobValue(g.waitFor, g.wait), Clock: clk,
	})
}

// knobValue is a policy's setting in the app that runs it — its default
// before —, or the code's value when the operation declared no setting.
func knobValue[T SettingValue](st *SettingService[T], def T) T {
	if st == nil {
		return def
	}
	return st.Get()
}

// formatRate spells a rate as the graph shows it.
func formatRate(rate float64) string { return strconv.FormatFloat(rate, 'f', -1, 64) }

// declarePolicySetting declares in svc the setting
// <service>-<operation>-<knob> of a policy of the operation n, with def as its
// default and check judging its resolved value; nil, with a declaration
// error, when the name is not one a setting may take or the service declares
// it already.
func declarePolicySetting[T SettingValue](svc *Service, n *nodeBase, knob string, def T, check func(v any) phrase) *SettingService[T] {
	name := policySettingName(svc.name, n.name, knob)
	if secret.ValidateName(name) != nil {
		svc.problem(n.decl, n.id, "node.setting-name", "node", n.name, "setting", name, "max", secret.MaxNameLen)
		return nil
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	for _, d := range svc.settings {
		if d.base().name == name {
			svc.diags = append(svc.diags, diagnostic{
				severity: "error", node: n.id, at: n.decl,
				message: say("policy.setting-twice", "node", n.id, "setting", name),
			})
			return nil
		}
	}
	st := NewSettingService(def)
	st.svc, st.name, st.decl, st.check = svc, name, n.decl, check
	st.id = svc.name + "/setting/" + name
	svc.settings = append(svc.settings, st)
	return st
}

// policySettingName is <service>-<operation>-<knob>, each name spelled as a
// setting's: "shop", "CreateItem", "rate" make "shop-create-item-rate".
func policySettingName(service, operation, knob string) string {
	return settingWords(service) + "-" + settingWords(operation) + "-" + knob
}

// settingWords spells a name as a setting's: lower case, a capital after a
// lower-case letter or a digit starting a word of its own, and every run of
// characters a setting's name cannot hold one dash, none at either end —
// "CreateItem" is "create-item", "GET /items/{id}" is "get-items-id".
func settingWords(name string) string {
	var b strings.Builder
	dash, prevLower := false, false
	for _, r := range name {
		lower, keep, starts := wordRune(r, prevLower)
		if !keep {
			dash, prevLower = true, false
			continue
		}
		if (dash || starts) && b.Len() > 0 {
			b.WriteByte('-')
		}
		dash, prevLower = false, lower == r
		b.WriteRune(lower)
	}
	return b.String()
}

// wordRune is r as a setting's name holds it: in lower case, whether it is
// kept at all, and whether it starts a word — a capital after a lower-case
// letter or a digit.
func wordRune(r rune, prevLower bool) (lower rune, keep, starts bool) {
	switch {
	case r >= 'A' && r <= 'Z':
		return r + ('a' - 'A'), true, prevLower
	case isLowerOrDigit(r):
		return r, true, false
	}
	return r, false, false
}

// isLowerOrDigit reports whether r is an ASCII lower-case letter or digit.
func isLowerOrDigit(r rune) bool { return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' }

// The checks of the policies' settings, made when a policy declares them:
// none runs at a product's initialisation.

// checkRate refuses a rate that is not a finite number above zero.
func checkRate() func(v any) phrase {
	return judged(func(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) && f > 0 }, func() phrase { return say("policy.rate") })
}

// checkAtLeastOne refuses a count below one.
func checkAtLeastOne() func(v any) phrase {
	return judged(func(n int) bool { return n >= 1 }, func() phrase { return say("policy.at-least-one") })
}

// checkNotNegative refuses a negative count.
func checkNotNegative() func(v any) phrase {
	return judged(func(n int) bool { return n >= 0 }, func() phrase { return say("policy.not-negative") })
}

// checkPositive refuses a duration that is not positive.
func checkPositive() func(v any) phrase {
	return judged(func(d time.Duration) bool { return d > 0 }, func() phrase { return say("policy.positive") })
}

// checkWait refuses a negative wait.
func checkWait() func(v any) phrase {
	return judged(func(d time.Duration) bool { return d >= 0 }, func() phrase { return say("policy.wait") })
}

// judged is a setting's check: refused says why a value of type T is not
// one ok accepts.
func judged[T SettingValue](ok func(v T) bool, refused func() phrase) func(v any) phrase {
	return func(v any) phrase {
		if t, isT := v.(T); isT && !ok(t) {
			return refused()
		}
		return phrase{}
	}
}
