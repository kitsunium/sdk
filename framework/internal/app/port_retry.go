package app

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// The knobs of a port's retry and of its budget, each a suffix of its
// setting's name.
const (
	knobRetryAttempts = "retry-attempts"
	knobRetryDelay    = "retry-delay"
	knobRetryMaxDelay = "retry-max-delay"
	knobRetryJitter   = "retry-jitter"
	knobRetryAfter    = "retry-after"
	knobBudgetTokens  = "budget-tokens"
	knobBudgetRatio   = "budget-ratio"
)

// The jitters a retry's setting names: the SDK's full and decorrelated
// jitters, and none.
const (
	jitterFull         = "full"
	jitterDecorrelated = "decorrelated"
	jitterNone         = "none"
)

// defaultRetryMaxDelay is the longest a retry waits when the code names no
// longer delay: a server's Retry-After beyond it is cut to it.
const defaultRetryMaxDelay time.Duration = 30 * time.Second

// minBudgetRatio is the smallest token ratio the SDK's retry budget reads:
// a thousandth, as gRPC reads it.
const minBudgetRatio float64 = 0.001

// retryGuard is a port's retry ([Retry]).
type retryGuard struct {
	attempts    int
	delay       time.Duration
	maxDelay    time.Duration
	honour      bool
	attemptsFor *SettingService[int]
	delayFor    *SettingService[time.Duration]
	maxDelayFor *SettingService[time.Duration]
	jitterFor   *SettingService[string]
	afterFor    *SettingService[bool]
	config      perStart[resilience.RetryConfig]
}

// retryOption configures a retry.
type retryOption func(g *retryGuard)

// retryConfigure sets the option on what it configures.
func (f retryOption) retryConfigure(g *retryGuard) { f(g) }

// Retry runs a port's call again when it fails for a reason of the
// dependency's — what the start answers as a 5xx, a 408 or a 429 —, up to
// maxAttempts attempts in all, waiting between them a backoff that starts at
// delay and doubles, with full jitter, up to [RetryMaxDelay] — 30 seconds
// unless the code says otherwise. A failure that says how long to wait — a
// Retry-After, a rate limiter's next token (the SDK's resilience.RetryAfter)
// — is waited that long instead, held at the longest delay, unless
// [NoRetryAfter]. A refusal of the caller's own is not retried, nor an open
// breaker, a throttle or another retry's exhaustion. It is the SDK's
// resilience retry, on the app's clock, sharing the port's retry budget
// ([Budget]) with its hedge.
//
// Each retry is an event on the port's span, which counts them, and a
// retry event on the telemetry port.
//
// The values are the defaults of the port's settings
// <service>-port-<name>-retry-attempts, -retry-delay, -retry-max-delay,
// -retry-jitter (full, decorrelated or none) and -retry-after (whether a
// failure's own delay is honoured), which config/<env>.* and
// <PRODUCT>_<NAME> override; fewer than one attempt, or a delay that is not
// positive, stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Retry(maxAttempts int, delay time.Duration, opts ...RetryConfigurer) PortPolicyConfigurer {
	return portPolicyOption(func(g *portGuards) {
		r := &retryGuard{attempts: maxAttempts, delay: delay, maxDelay: max(defaultRetryMaxDelay, delay), honour: true}
		for _, o := range opts {
			if o != nil {
				o.retryConfigure(r)
			}
		}
		g.add(r)
	})
}

// RetryMaxDelay is the longest a [Retry] waits between two attempts: the
// backoff's cap, and the cap on a delay a failure asks for. It is the default
// of the port's setting <service>-port-<name>-retry-max-delay; one that is
// not positive stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func RetryMaxDelay(d time.Duration) RetryConfigurer {
	return retryOption(func(g *retryGuard) { g.maxDelay = d })
}

// NoRetryAfter makes a [Retry] wait its backoff whatever a failure says: a
// Retry-After, a rate limiter's next token. It is the default of the port's
// setting <service>-port-<name>-retry-after, false, which config/<env>.* and
// <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func NoRetryAfter() RetryConfigurer {
	return retryOption(func(g *retryGuard) { g.honour = false })
}

// rank is the retry's place: the outermost.
func (*retryGuard) rank() int { return rankRetry }

// declare declares the retry's settings.
func (g *retryGuard) declare(svc *Service, n *nodeBase) {
	g.attemptsFor = declarePolicySetting(svc, n, knobRetryAttempts, g.attempts, checkAtLeastOne())
	g.delayFor = declarePolicySetting(svc, n, knobRetryDelay, g.delay, checkPositive())
	g.maxDelayFor = declarePolicySetting(svc, n, knobRetryMaxDelay, g.maxDelay, checkPositive())
	g.jitterFor = declarePolicySetting(svc, n, knobRetryJitter, jitterFull, checkJitter())
	g.afterFor = declarePolicySetting(svc, n, knobRetryAfter, g.honour, nil)
}

// mechanic draws the retry.
func (g *retryGuard) mechanic() model.Mechanic {
	attempts, delay := knobValue(g.attemptsFor, g.attempts), knobValue(g.delayFor, g.delay)
	maxDelay, jitter := knobValue(g.maxDelayFor, g.maxDelay), knobValue(g.jitterFor, jitterFull)
	honour := knobValue(g.afterFor, g.honour)
	m := model.Mechanic{
		Kind: "retry", Label: fmt.Sprintf("%d attempts from %s", attempts, delay), Package: resiliencePackage,
		Config: map[string]string{"attempts": strconv.Itoa(attempts), "delay": delay.String(), "maxDelay": maxDelay.String()},
	}
	m.Config["jitter"], m.Config["retryAfter"] = jitter, strconv.FormatBool(honour)
	return m
}

// runner is the SDK's retry for one call: its configuration is the start's,
// its OnRetry the call's.
func (g *retryGuard) runner(c *portCall) resilience.Runner {
	cfg := g.config.get(c.a, func(clk clock.Timed) resilience.RetryConfig { return g.configOf(c.a, clk) })
	cfg.Budget, cfg.OnRetry = c.budget, c.retried
	return resilience.NewRetry(cfg)
}

// configOf is the retry's configuration in app a's start, on clk.
func (g *retryGuard) configOf(a *App, clk clock.Timed) resilience.RetryConfig {
	cfg := resilience.RetryConfig{
		MaxAttempts: knobIn(a, g.attemptsFor, g.attempts), BaseDelay: knobIn(a, g.delayFor, g.delay),
		MaxDelay: knobIn(a, g.maxDelayFor, g.maxDelay), Retryable: transient, Clock: clk,
	}
	switch knobIn(a, g.jitterFor, jitterFull) {
	case jitterFull:
		cfg.JitterMode = resilience.JitterFull
	case jitterDecorrelated:
		cfg.JitterMode = resilience.JitterDecorrelated
	}
	if knobIn(a, g.afterFor, g.honour) {
		cfg.Delay = failureDelay
	}
	return cfg
}

// failureDelay is the wait a failure asks for, as RetryConfig.Delay reads it.
func failureDelay(_ int, err error) (time.Duration, bool) { return resilience.RetryAfter(err) }

// started has nothing to ready.
func (*retryGuard) started(_ *App, _ string) phrase { return phrase{} }

// budgetGuard is a port's retry budget ([Budget]).
type budgetGuard struct {
	tokens    int
	ratio     float64
	tokensFor *SettingService[int]
	ratioFor  *SettingService[float64]
	built     perStart[resilience.RetryBudget]
}

// Budget sizes the retry budget a port's [Retry] and [Hedge] share: a
// bucket of maxTokens tokens, a failure costing one and a success putting
// back tokenRatio; retries and duplicates are refused once half the tokens
// are spent, so a dependency that fails for every caller receives fewer
// extra calls, not every call times the attempts. A port that retries or
// hedges without it has 10 tokens and a ratio of 0.1, gRPC's example; a
// Budget on a port that does neither stops the start. It is the SDK's
// resilience retry budget, one per port and per start.
//
// The values are the defaults of the port's settings
// <service>-port-<name>-budget-tokens and -budget-ratio, which
// config/<env>.* and <PRODUCT>_<NAME> override; fewer than one token, or a
// ratio below a thousandth, stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Budget(maxTokens int, tokenRatio float64) PortPolicyConfigurer {
	return portPolicyOption(func(g *portGuards) {
		g.budgets = append(g.budgets, &budgetGuard{tokens: maxTokens, ratio: tokenRatio})
	})
}

// declare declares the budget's settings.
func (b *budgetGuard) declare(svc *Service, n *nodeBase) {
	b.tokensFor = declarePolicySetting(svc, n, knobBudgetTokens, b.tokens, checkAtLeastOne())
	b.ratioFor = declarePolicySetting(svc, n, knobBudgetRatio, b.ratio, checkBudgetRatio())
}

// mechanic draws the budget.
func (b *budgetGuard) mechanic() model.Mechanic {
	tokens, ratio := knobValue(b.tokensFor, b.tokens), formatRate(knobValue(b.ratioFor, b.ratio))
	return model.Mechanic{
		Kind: "budget", Label: fmt.Sprintf("%d tokens, %s a success", tokens, ratio), Package: resiliencePackage,
		Config: map[string]string{"tokens": strconv.Itoa(tokens), "ratio": ratio},
	}
}

// of is the budget of app a's start.
func (b *budgetGuard) of(a *App) resilience.RetryBudget {
	return b.built.get(a, func(clock.Timed) resilience.RetryBudget {
		return resilience.NewRetryBudget(resilience.RetryBudgetConfig{
			MaxTokens: knobIn(a, b.tokensFor, b.tokens), TokenRatio: knobIn(a, b.ratioFor, b.ratio),
		})
	})
}

// checkJitter refuses a jitter the retry does not know.
func checkJitter() func(v any) phrase {
	return judged(func(s string) bool { return s == jitterFull || s == jitterDecorrelated || s == jitterNone },
		func() phrase { return say("policy.jitter") })
}

// checkBudgetRatio refuses a ratio below a thousandth, or not a number.
func checkBudgetRatio() func(v any) phrase {
	return judged(func(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) && f >= minBudgetRatio },
		func() phrase { return say("policy.budget-ratio") })
}
