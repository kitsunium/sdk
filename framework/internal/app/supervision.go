package app

import (
	"context"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/health"
	"github.com/kitsunium/sdk/pkg/v1/app/lifecycle"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// When the framework stops restarting what fails (issue #483, part 4): a
// subscription's, a watch's and a queued command's consumer, a hand-written
// loop (Service.Go) and a declared loop (Service.Loop) each give up once
// they failed more than <restarts> times within <within> — Erlang's restart
// intensity and period, the SDK's SupervisorConfig.GiveUpAfter (ADR 0184) —,
// and the product stops being live: one liveness check per component,
// "supervised:<node>", answers the supervisor's Check, so a probe — an
// orchestrator — restarts the process, the level above deciding what a loop
// that cannot succeed does next. A product nothing probes keeps running
// without the component: its log, its problems, its loop in the Studio and
// its telemetry say so.
//
// Liveness, not readiness: a supervision that gave up never runs its
// function again in this process — nothing starts it again —, so a probe
// that took the product out of rotation without restarting it would leave a
// process that no longer works, forever; and a daemon whose consumers gave up
// serves no traffic that readiness could hold back.
//
// Each declares, in its service, part 1's two settings (policy.go):
//
//	<service>-<kind>-<name>-give-up-restarts  the early ends the span tolerates
//	<service>-<kind>-<name>-give-up-within    the span; 0 never gives up
//
// The kind is subscription — a watch's too —, command or loop. The code's
// value — defaultGiveUpRestarts within defaultGiveUpWithin unless
// [GiveUpAfter] says otherwise — is the default; a negative value stops the
// start.
//
// An early end is a run of the consumer's queue or of the hand-written loop
// that returned while the app still served, and, for a declared loop, which
// the framework runs and never sees return, a run that failed or panicked:
// each is followed by the 1 s to 1 min backoff, and each counts.

// The knobs of a supervision, each a suffix of its setting's name.
const (
	knobGiveUpRestarts = "give-up-restarts"
	knobGiveUpWithin   = "give-up-within"
)

// The code's restart intensity: ten early ends within ten minutes. Erlang's
// default — one restart in five seconds — counts restarts that are
// immediate; the framework's back off from one second to a minute, so the
// span covers the curve: a function that fails at every start is given up
// about five minutes after its first failure (1+2+4+8+16+32+60+60+60+60 s
// of backoff before its eleventh), while one that fails now and then — once
// a minute or less often — is restarted forever.
const (
	defaultGiveUpRestarts int           = 10
	defaultGiveUpWithin   time.Duration = 10 * time.Minute
)

// supervisedCheck is the prefix of a supervised component's liveness check.
const supervisedCheck = "supervised:"

// giveUpKnobs are a supervision's restart intensity: the code's values, and
// the settings that override them once its declaration declared them.
type giveUpKnobs struct {
	restarts    int
	within      time.Duration
	restartsFor *SettingService[int]
	withinFor   *SettingService[time.Duration]
}

// defaultGiveUp is a supervision's intensity until an option says otherwise.
func defaultGiveUp() giveUpKnobs {
	return giveUpKnobs{restarts: defaultGiveUpRestarts, within: defaultGiveUpWithin}
}

// declare declares the supervision of n's settings in svc.
func (k *giveUpKnobs) declare(svc *Service, n *nodeBase) {
	k.restartsFor = declarePolicySetting(svc, n, knobGiveUpRestarts, k.restarts, checkNotNegative())
	k.withinFor = declarePolicySetting(svc, n, knobGiveUpWithin, k.within, checkGiveUpWithin())
}

// value is the intensity in the app that runs it — its default before —, as
// the SDK's supervisor takes it: the zero, which never gives up, for a span
// of zero.
func (k *giveUpKnobs) value() lifecycle.GiveUp {
	within := knobValue(k.withinFor, k.within)
	if within <= 0 {
		return lifecycle.GiveUp{}
	}
	return lifecycle.GiveUp{Restarts: knobValue(k.restartsFor, k.restarts), Within: within}
}

// giveUpText is the intensity as the graph and the logs say it.
func giveUpText(g lifecycle.GiveUp) string {
	if g.Within <= 0 {
		return "never gives up"
	}
	return "gives up after " + strconv.Itoa(g.Restarts) + " within " + g.Within.String()
}

// checkGiveUpWithin refuses a negative span.
func checkGiveUpWithin() func(v any) phrase {
	return judged(func(d time.Duration) bool { return d >= 0 }, func() phrase { return say("policy.give-up-within") })
}

// routineOptions are a hand-written loop's options.
type routineOptions struct {
	giveUp giveUpKnobs
}

// info is a hand-written loop's node as the graph draws it: supervised,
// restarted after a failure until it gives up.
func (o *routineOptions) info() *model.LoopInfo {
	return &model.LoopInfo{Style: model.LoopGoroutine, Restart: restartText("after an error or a panic", o.giveUp.value())}
}

// giveUpOption is [GiveUpAfter].
type giveUpOption struct {
	restarts int
	within   time.Duration
}

// set puts the intensity in k.
func (g giveUpOption) set(k *giveUpKnobs) { k.restarts, k.within = g.restarts, g.within }

// subscriptionConfigure sets the option on what it configures.
func (g giveUpOption) subscriptionConfigure(o *subscriptionOptions) { g.set(&o.knobs.giveUp) }

// commandConfigure sets the option on a queued command: a command that is
// not queued refuses it, as it refuses every delivery option.
func (g giveUpOption) commandConfigure(o *commandOptions) {
	o.deliveriesGiven = true
	g.set(&o.deliveries.knobs.giveUp)
}

// loopConfigure sets the option on what it configures.
func (g giveUpOption) loopConfigure(o *loopOptions) { g.set(&o.giveUp) }

// routineConfigure sets the option on what it configures.
func (g giveUpOption) routineConfigure(o *routineOptions) { g.set(&o.giveUp) }

// GiveUpAfter bounds how often the framework restarts what fails — a
// subscription's, a watch's or a queued command's consumer whose queue
// failed, a hand-written loop ([Service.Go]) that returned, a declared
// loop ([Service.Loop]) whose run failed or panicked —: once it failed
// more than restarts times within within, it is not restarted again, and
// the product is no longer live — its liveness check
// "supervised:<node>" fails, the problem names it — so that whatever
// watches its liveness — an orchestrator's probe — restarts the process; a
// product nothing probes keeps running without it, and says so in its log,
// its problems and its telemetry. It is Erlang's restart
// intensity, the SDK's lifecycle supervisor's GiveUpAfter.
//
// The default is 10 within 10 minutes: with the restarts' backoff — one
// second, doubling up to a minute — a component that fails at every start
// is given up about five minutes after its first failure, and one that
// fails once a minute or less often is restarted forever. A within of zero
// never gives up; a negative value stops the start.
//
// restarts and within are the defaults of the settings
// <service>-<kind>-<name>-give-up-restarts and -give-up-within, the kind
// subscription, command or loop, which config/<env>.* and <PRODUCT>_<NAME>
// override.
//
// IFACE-OPAQUE: the option is sealed — its methods are unexported — so a
// caller only hands it to the declaration it configures.
func GiveUpAfter(restarts int, within time.Duration) SupervisionConfigurer {
	return giveUpOption{restarts: restarts, within: within}
}

// supervise registers the liveness check of the component node, which check
// answers: nil while it runs, its gave-up error once it gave up. A run that
// answers no probe — a CLI command's — registers none.
func (a *App) supervise(node string, check func() error) error {
	if a.health == nil {
		return nil
	}
	return a.health.AddLiveness(health.LivenessCheck{Name: supervisedCheck + node, Check: check})
}

// supervisorCheck is the liveness check of a component the SDK's
// supervisor runs: its Check.
func supervisorCheck(sup *lifecycle.Supervisor) func() error {
	return func() error { return sup.Check(context.Background()) }
}

// gaveUp records that the component node, whose loop is l, gave up: its
// loop stops and says why, the problem and the error log name it, and the
// telemetry port hears it. err is the gave-up error its check answers.
func (a *App) gaveUp(l *loopState, node string, g lifecycle.GiveUp, err error) {
	l.givenUp(a, err)
	a.problem(node, say("supervision.gave-up", "node", node, "restarts", g.Restarts, "within", g.Within.String(),
		"check", supervisedCheck+node))
	logger.Error(context.Background(), a.log, "the framework gave up restarting "+node+": the product is no longer live",
		logger.String("node", node), logger.Int("restarts", g.Restarts), logger.String("within", g.Within.String()),
		logger.String("error", errs.PublicOf(err)))
	a.reportGaveUp(node, err)
}

// loopGaveUp is the error a declared loop's liveness check answers once it
// gave up.
func loopGaveUp(node string, g lifecycle.GiveUp, last error) error {
	fields := []errs.Field{
		errs.String("loop", node), errs.Int("restarts", g.Restarts), errs.String("within", g.Within.String()),
	}
	if last != nil {
		fields = append(fields, errs.String("last", errs.PublicOf(last)))
	}
	return failure(CodeGaveUp, "GAVE_UP", "a loop failed too often and the framework stopped running it", nil, fields...)
}

// restartText is how a supervised loop's restart reads in the graph.
func restartText(what string, g lifecycle.GiveUp) string {
	return what + ", backing off from 1s to 1m; " + giveUpText(g)
}

// endsWithin adds the failure at to ends and drops those a span of within
// before it no longer reaches, in place, so that ends never holds more than
// one span's failures; without a span it keeps nothing. It is the rule of
// the SDK's supervisor (GiveUpAfter), for a declared loop, whose failures
// the framework counts itself: it runs the loop, and never sees it return.
func endsWithin(ends []time.Time, at time.Time, within time.Duration) []time.Time {
	if within <= 0 {
		return ends[:0]
	}
	stale := 0
	for stale < len(ends) && at.Sub(ends[stale]) >= within {
		stale++
	}
	ends = ends[:copy(ends, ends[stale:])]
	return append(ends, at)
}

// failed counts a declared loop's run r that ended at ended with err, its
// next run held until floor: one more failure within g's span — the
// telemetry port hears the restart —, or the one past g, which gives the
// loop up and stops it. A run that did not fail changes nothing. It reports
// whether the loop stops.
func (l *Loop) failed(r *loopRun, g lifecycle.GiveUp, ended, floor time.Time, err error) bool {
	if err == nil {
		return false
	}
	r.ends = endsWithin(r.ends, ended, g.Within)
	if g.Within > 0 && len(r.ends) > g.Restarts {
		l.giveUp(r, g, err)
		return true
	}
	r.a.reportRestart(l.id, err, floor.Sub(ended))
	return false
}

// giveUp stops the declared loop's run r, whose runs failed past g, the
// last with last: its liveness check answers the gave-up error from now on.
func (l *Loop) giveUp(r *loopRun, g lifecycle.GiveUp, last error) {
	err := loopGaveUp(l.id, g, last)
	r.a.gaveUp(r.state, l.id, g, err)
	l.mu.Lock()
	r.gaveUp = err
	l.mu.Unlock()
}

// gaveUpOf is the declared loop's run r's liveness: its gave-up error, nil
// while it runs.
func (l *Loop) gaveUpOf(r *loopRun) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return r.gaveUp
}

// givenUp records that the loop's failures stopped it: its state is
// stopped and its last error says so, in the framework's words; its
// failures were counted as they came.
func (l *loopState) givenUp(a *App, err error) {
	a.mu.Lock()
	l.State = model.LoopStopped
	l.active = 0
	l.NextRun = nil
	_, body := describe(err)
	l.LastError = body.Message
	snapshot := l.Loop
	a.mu.Unlock()
	a.hub.publish(model.Event{Type: model.EventLoop, Loop: &snapshot})
}
