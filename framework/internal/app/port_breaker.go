package app

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// The knobs of a port's breaker, each a suffix of its setting's name.
const (
	knobBreakerFailures    = "breaker-failures"
	knobBreakerOpen        = "breaker-open"
	knobBreakerOpenMax     = "breaker-open-max"
	knobBreakerTrials      = "breaker-trials"
	knobBreakerWindow      = "breaker-window"
	knobBreakerWindowCalls = "breaker-window-calls"
	knobBreakerWindowRatio = "breaker-window-ratio"
)

// The window a breaker counts on when the code gives none: no window — the
// failures in a row decide —, and, once a configuration gives one a size,
// Polly's MinimumThroughput and FailureRatio.
const (
	defaultBreakerWindowCalls int     = 100
	defaultBreakerWindowRatio float64 = 0.1
)

// breakerGuard is a port's circuit breaker ([Breaker]).
type breakerGuard struct {
	failures       int
	open           time.Duration
	openMax        time.Duration
	trials         int
	window         time.Duration
	windowCalls    int
	windowRatio    float64
	failuresFor    *SettingService[int]
	openFor        *SettingService[time.Duration]
	openMaxFor     *SettingService[time.Duration]
	trialsFor      *SettingService[int]
	windowFor      *SettingService[time.Duration]
	windowCallsFor *SettingService[int]
	windowRatioFor *SettingService[float64]
	// node is the port's: its ID names the breaker in a log line, the
	// readiness and the telemetry, read when it is used — a module renames
	// it.
	node  *nodeBase
	built perStart[resilience.Runner]
}

// breakerOption configures a breaker.
type breakerOption func(g *breakerGuard)

// breakerConfigure sets the option on what it configures.
func (f breakerOption) breakerConfigure(g *breakerGuard) { f(g) }

// Breaker stops a port's calls from reaching a dependency that keeps
// failing: after failures failures of the dependency's in a row — or, given
// [BreakerWindow], a share of a window's calls — it opens, and every call
// fails at once with the SDK's CircuitOpen (503) for open; then it lets
// [HalfOpenTrials] trial calls through, one at a time, which close it or open
// it again, for longer each time under [OpenBackoff]. A refusal of the
// caller's own does not count, nor a caller that left, nor a throttle's
// refusal. It is the SDK's resilience circuit breaker, on the app's clock,
// one per port and per start.
//
// Its state is the product's: a readiness check, breaker:<port>, degraded —
// never unready — while it is not closed; each change of phase logged and
// put on the telemetry port. An operator opens it until reset with the
// product's `breaker isolate PORT` and closes it with `breaker reset PORT`,
// kept in the data directory: a start applies it, and so does a running
// product, within five seconds.
//
// The values are the defaults of the port's settings
// <service>-port-<name>-breaker-failures, -breaker-open, -breaker-open-max,
// -breaker-trials, -breaker-window, -breaker-window-calls and
// -breaker-window-ratio, which config/<env>.* and <PRODUCT>_<NAME> override;
// a value the breaker cannot run with stops the start, and so does a
// breaker the SDK refuses.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Breaker(failures int, open time.Duration, opts ...BreakerConfigurer) PortPolicyConfigurer {
	return portPolicyOption(func(g *portGuards) {
		b := &breakerGuard{
			failures: failures, open: open, trials: 1,
			windowCalls: defaultBreakerWindowCalls, windowRatio: defaultBreakerWindowRatio,
		}
		for _, o := range opts {
			if o != nil {
				o.breakerConfigure(b)
			}
		}
		g.add(b)
	})
}

// BreakerWindow makes a [Breaker] open on a rate rather than on failures in
// a row: once the last size of calls holds at least minCalls, a share of
// failures of failureRatio or more opens it. Its values are the defaults of
// the port's settings <service>-port-<name>-breaker-window,
// -breaker-window-calls and -breaker-window-ratio; a window of 0 is none, a
// ratio outside (0, 1] stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func BreakerWindow(size time.Duration, minCalls int, failureRatio float64) BreakerConfigurer {
	return breakerOption(func(g *breakerGuard) { g.window, g.windowCalls, g.windowRatio = size, minCalls, failureRatio })
}

// HalfOpenTrials is how many trial calls in a row must succeed to close a
// [Breaker] once its open time is over — one by default. It is the default
// of the port's setting <service>-port-<name>-breaker-trials; fewer than one
// stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func HalfOpenTrials(n int) BreakerConfigurer {
	return breakerOption(func(g *breakerGuard) { g.trials = n })
}

// OpenBackoff lengthens the open time of a [Breaker] that opens again
// without having closed: it doubles at each opening, from the breaker's open
// time up to max. It is the default of the port's setting
// <service>-port-<name>-breaker-open-max, 0 — the same open time every
// time — without it; a negative one stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OpenBackoff(maxOpen time.Duration) BreakerConfigurer {
	return breakerOption(func(g *breakerGuard) { g.openMax = maxOpen })
}

// rank is the breaker's place: inside the retry and the hedge.
func (*breakerGuard) rank() int { return rankBreaker }

// declare declares the breaker's settings.
func (g *breakerGuard) declare(svc *Service, n *nodeBase) {
	g.node = n
	g.failuresFor = declarePolicySetting(svc, n, knobBreakerFailures, g.failures, checkAtLeastOne())
	g.openFor = declarePolicySetting(svc, n, knobBreakerOpen, g.open, checkPositive())
	g.openMaxFor = declarePolicySetting(svc, n, knobBreakerOpenMax, g.openMax, checkWait())
	g.trialsFor = declarePolicySetting(svc, n, knobBreakerTrials, g.trials, checkAtLeastOne())
	g.windowFor = declarePolicySetting(svc, n, knobBreakerWindow, g.window, checkWait())
	g.windowCallsFor = declarePolicySetting(svc, n, knobBreakerWindowCalls, g.windowCalls, checkAtLeastOne())
	g.windowRatioFor = declarePolicySetting(svc, n, knobBreakerWindowRatio, g.windowRatio, checkRatio())
}

// mechanic draws the breaker.
func (g *breakerGuard) mechanic() model.Mechanic {
	failures, open := knobValue(g.failuresFor, g.failures), knobValue(g.openFor, g.open)
	config := map[string]string{
		"failures": strconv.Itoa(failures), "open": open.String(),
		"trials": strconv.Itoa(knobValue(g.trialsFor, g.trials)),
	}
	label := fmt.Sprintf("%d failures, open %s", failures, open)
	if openMax := knobValue(g.openMaxFor, g.openMax); openMax > 0 {
		config["openMax"] = openMax.String()
	}
	if window := knobValue(g.windowFor, g.window); window > 0 {
		calls, ratio := knobValue(g.windowCallsFor, g.windowCalls), formatRate(knobValue(g.windowRatioFor, g.windowRatio))
		config["window"], config["windowCalls"], config["windowRatio"] = window.String(), strconv.Itoa(calls), ratio
		label = fmt.Sprintf("%s of %d calls in %s, open %s", ratio, calls, window, open)
	}
	return model.Mechanic{Kind: "breaker", Label: label, Package: resiliencePackage, Config: config}
}

// runner is the breaker of the call's start.
func (g *breakerGuard) runner(c *portCall) resilience.Runner { return g.of(c.a) }

// of is the breaker of app a's start, built from the settings it resolved,
// on its clock.
func (g *breakerGuard) of(a *App) resilience.Runner {
	return g.built.get(a, func(clk clock.Timed) resilience.Runner { return resilience.NewCircuitBreaker(g.configOf(a, clk)) })
}

// configOf is the breaker's configuration in app a's start, on clk.
func (g *breakerGuard) configOf(a *App, clk clock.Timed) resilience.BreakerConfig {
	open := knobIn(a, g.openFor, g.open)
	cfg := resilience.BreakerConfig{
		FailureThreshold: knobIn(a, g.failuresFor, g.failures), OpenDuration: open, Clock: clk,
		Retryable: transient, HalfOpenTrials: knobIn(a, g.trialsFor, g.trials),
		OnStateChange: func(from, to resilience.BreakerPhase) { a.breakerChanged(g.id(), from, to) },
	}
	if openMax := knobIn(a, g.openMaxFor, g.openMax); openMax > 0 {
		cfg.OpenBackoff = &resilience.Backoff{BaseDelay: open, MaxDelay: openMax}
	}
	if window := knobIn(a, g.windowFor, g.window); window > 0 {
		cfg.Window = &resilience.BreakerWindowValue{
			Size: window, MinCalls: knobIn(a, g.windowCallsFor, g.windowCalls), FailureRatio: knobIn(a, g.windowRatioFor, g.windowRatio),
		}
	}
	return cfg
}

// id is the port's ID.
func (g *breakerGuard) id() string {
	if g.node == nil {
		return ""
	}
	return g.node.id
}

// state is the breaker of app a's start; nil when the SDK refused it.
func (g *breakerGuard) state(a *App) resilience.BreakerState {
	st, _ := g.of(a).(resilience.BreakerState)
	return st
}

// started refuses a breaker the SDK refuses with the start's values: its
// every call would fail with PolicyMisconfigured, which is no state a
// breaker has.
func (g *breakerGuard) started(a *App, port string) phrase {
	r := g.of(a)
	if _, ok := r.(resilience.BreakerState); ok {
		return phrase{}
	}
	knob := ""
	err := r.Run(context.Background(), func(context.Context) error { return nil })
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "knob" {
			knob = f.StringValue()
		}
	}
	return say("policy.breaker-refused", "port", port, "knob", knob)
}

// breakerChanged is a breaker's change of phase: logged — a warning when it
// opens —, and put on the telemetry port.
func (a *App) breakerChanged(port string, from, to resilience.BreakerPhase) {
	attrs := []logger.Attr{logger.String("node", port), logger.String("from", from.String()), logger.String("to", to.String())}
	if to == resilience.BreakerOpen {
		logger.Warn(context.Background(), a.log, "the breaker of "+port+" opened: its calls fail at once", attrs...)
	} else {
		logger.Info(context.Background(), a.log, "the breaker of "+port+" is "+to.String(), attrs...)
	}
	a.reportBreaker(port, to)
}

// breakerReady is a breaker's readiness: an error, which degrades the
// product without taking it out of rotation, while it is not closed.
func breakerReady(port string, st resilience.BreakerState) error {
	if st == nil {
		return nil
	}
	switch s := st.State(); {
	case s.Isolated:
		return Unavailable("the breaker of " + port + " is isolated: breaker reset " + port + " closes it")
	case s.Phase != resilience.BreakerClosed:
		return Unavailable("the breaker of " + port + " is " + s.Phase.String())
	}
	return nil
}

// checkRatio refuses a share that is not above zero and at most one.
func checkRatio() func(v any) phrase {
	return judged(func(f float64) bool { return !math.IsNaN(f) && f > 0 && f <= 1 }, func() phrase { return say("policy.ratio") })
}
