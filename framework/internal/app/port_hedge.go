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

// The knobs of a port's hedge and of its throttle, each a suffix of its
// setting's name.
const (
	knobHedgeDelay    = "hedge-delay"
	knobHedgeMax      = "hedge-max"
	knobHedgeInFlight = "hedge-in-flight"
	knobThrottleK     = "throttle-k"
)

// minThrottleK is the smallest K the SDK's adaptive throttle takes.
const minThrottleK float64 = 1

// Idempotent declares that the port's operation may run more than once for
// one call, concurrently, with no harm done: what a [Hedge] needs, which is
// refused on a port that does not say so. The graph draws it on the port.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Idempotent() PortPolicyConfigurer {
	return portPolicyOption(func(g *portGuards) { g.idempotent = true })
}

// hedgeGuard is a port's hedge ([Hedge]).
type hedgeGuard struct {
	delay       time.Duration
	inFlight    int
	hedges      int
	delayFor    *SettingService[time.Duration]
	inFlightFor *SettingService[int]
	hedgesFor   *SettingService[int]
	built       perStart[resilience.Runner]
}

// Hedge races a port's slow call: an attempt that has not answered after
// delay is joined by a duplicate, and the first to succeed answers — the
// tail of a dependency's latency cut, at the price of more calls. At most
// inFlight duplicates run at once across every call, and the port's retry
// budget ([Budget]) refuses them while the dependency fails for many: then
// the call goes on with the attempts it has. Only a port declared
// [Idempotent] may hedge — the start refuses another. It is the SDK's
// resilience hedge, on the app's clock.
//
// The values are the defaults of the port's settings
// <service>-port-<name>-hedge-delay, -hedge-in-flight and -hedge-max — the
// duplicates one call may make, 1 —, which config/<env>.* and
// <PRODUCT>_<NAME> override; a delay that is not positive, or fewer than
// one, stops the start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Hedge(delay time.Duration, inFlight int) PortPolicyConfigurer {
	return portPolicyOption(func(g *portGuards) { g.add(&hedgeGuard{delay: delay, inFlight: inFlight, hedges: 1}) })
}

// rank is the hedge's place: inside the retry.
func (*hedgeGuard) rank() int { return rankHedge }

// declare declares the hedge's settings.
func (g *hedgeGuard) declare(svc *Service, n *nodeBase) {
	g.delayFor = declarePolicySetting(svc, n, knobHedgeDelay, g.delay, checkPositive())
	g.inFlightFor = declarePolicySetting(svc, n, knobHedgeInFlight, g.inFlight, checkAtLeastOne())
	g.hedgesFor = declarePolicySetting(svc, n, knobHedgeMax, g.hedges, checkAtLeastOne())
}

// mechanic draws the hedge.
func (g *hedgeGuard) mechanic() model.Mechanic {
	delay, inFlight, hedges := knobValue(g.delayFor, g.delay), knobValue(g.inFlightFor, g.inFlight), knobValue(g.hedgesFor, g.hedges)
	return model.Mechanic{
		Kind: "hedge", Label: fmt.Sprintf("after %s, %d in flight", delay, inFlight), Package: resiliencePackage,
		Config: map[string]string{"delay": delay.String(), "inFlight": strconv.Itoa(inFlight), "max": strconv.Itoa(hedges)},
	}
}

// runner is the hedge of the call's start, sharing its budget.
func (g *hedgeGuard) runner(c *portCall) resilience.Runner {
	return g.built.get(c.a, func(clk clock.Timed) resilience.Runner {
		return resilience.NewHedge(resilience.HedgeConfig{
			Idempotent: true, Delay: knobIn(c.a, g.delayFor, g.delay), MaxHedges: knobIn(c.a, g.hedgesFor, g.hedges),
			MaxInFlight: knobIn(c.a, g.inFlightFor, g.inFlight), Clock: clk, Budget: c.budget,
		})
	})
}

// started has nothing to ready.
func (*hedgeGuard) started(_ *App, _ string) phrase { return phrase{} }

// throttleGuard is a port's adaptive throttle ([Throttle]).
type throttleGuard struct {
	k    float64
	kFor *SettingService[float64]
	// random draws each refusal; nil is the SDK's. A test sets it.
	random func() float64
}

// Throttle refuses, on the caller's side and without sending them, a share
// of a port's calls while the dependency refuses more than 1/k of the
// recent ones — the client-side throttling of Google's SRE book: a call is
// refused with probability (requests − k × accepts) / (requests + 1), over
// the last two minutes, with the SDK's Throttled (503). A refusal is a 5xx,
// a 408 or a 429 of the dependency's; a refusal of the caller's own is an
// accept. It is the SDK's resilience adaptive throttle, on the app's clock;
// 2 is the book's k.
//
// k is the default of the port's setting <service>-port-<name>-throttle-k,
// which config/<env>.* and <PRODUCT>_<NAME> override; a k below 1 stops the
// start.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Throttle(k float64) PortPolicyConfigurer {
	return portPolicyOption(func(g *portGuards) {
		g.add(&policyLayer{p: policy{guard: &throttleGuard{k: k}}, place: rankThrottle})
	})
}

// declare declares <port>-throttle-k.
func (g *throttleGuard) declare(svc *Service, n *nodeBase) {
	g.kFor = declarePolicySetting(svc, n, knobThrottleK, g.k, checkThrottleK())
}

// mechanic draws the throttle.
func (g *throttleGuard) mechanic() model.Mechanic {
	k := formatRate(knobValue(g.kFor, g.k))
	return model.Mechanic{Kind: "throttle", Label: "k " + k, Package: resiliencePackage, Config: map[string]string{"k": k}}
}

// runner is the SDK's adaptive throttle.
func (g *throttleGuard) runner(clk clock.Timed) resilience.Runner {
	return resilience.NewAdaptiveThrottle(resilience.AdaptiveThrottleConfig{
		K: knobValue(g.kFor, g.k), Rejected: transient, Clock: clk, Random: g.random,
	})
}

// checkThrottleK refuses a K below 1, or not a number.
func checkThrottleK() func(v any) phrase {
	return judged(func(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) && f >= minThrottleK },
		func() phrase { return say("policy.throttle-k") })
}
