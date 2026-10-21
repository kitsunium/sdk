package app

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/lifecycle"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// A consumer's stop and lease, as settings (issue #483, part 3): a
// subscription, a watch and a queued command declare, in their service, the
// settings that say how their consumer stops and how long it holds a
// message, part 1's mechanism (policy.go) — the code's value the default,
// config/<env>.* and <PRODUCT>_<NAME> overriding it, a key nobody declares
// refused:
//
//	<service>-<kind>-<name>-drain        how long a handler running at a stop may still run
//	<service>-<kind>-<name>-settle       the bound on each outcome sent once the stop began
//	<service>-<kind>-<name>-lease        how long a delivery is leased
//	<service>-<kind>-<name>-auto-extend  whether a running handler's lease is renewed
//
// The kind is subscription — a watch's too — or command. A mailer declares
// none: its outbox is the mail spool's, which parks a mail with NackAfter and
// so must never have its lease renewed behind its back.
//
// The SDK's consumer (queue.ConsumerConfig) runs them: DrainTimeout,
// SettleTimeout, AutoExtend, and the queue policy's VisibilityTimeout. The
// app's per-component stop budget covers the longest drain plus its settle
// (componentStopBudget), and a stop tells every consumer at once, before the
// components stop one by one (beginDrains): the drains overlap instead of
// adding up.

// The knobs of a consumer, each a suffix of its setting's name.
const (
	knobDrain      = "drain"
	knobSettle     = "settle"
	knobLease      = "lease"
	knobAutoExtend = "auto-extend"
)

// The code's values of a consumer's knobs, unless its declaration says
// otherwise: a handler running at a stop gets five seconds to finish, each
// outcome after the stop five more — the SDK's DefaultSettleTimeout —, a
// delivery is leased thirty seconds and renewed while its handler runs.
const (
	defaultDrain  time.Duration = 5 * time.Second
	defaultSettle time.Duration = 5 * time.Second
	defaultLease  time.Duration = 30 * time.Second
)

// defaultStopBudget is the most one component's stop may take when no
// consumer's drain asks for more: the SDK lifecycle's per-component budget.
const defaultStopBudget time.Duration = 10 * time.Second

// runStopDeadline is how long the stop [App.Run] makes after a signal may
// take when no consumer's drain asks for more (stopDeadline).
const runStopDeadline time.Duration = 30 * time.Second

// stopMargin is what a component's stop budget keeps beyond a consumer's
// drain and settle: the time its supervisor takes to see its consumer end.
const stopMargin time.Duration = time.Second

// consumerKnobs are a consumer's stop and lease: the code's values, and the
// settings that override them once its declaration declared them.
type consumerKnobs struct {
	drain, settle, lease time.Duration
	autoExtend           bool

	drainFor, settleFor, leaseFor *SettingService[time.Duration]
	autoExtendFor                 *SettingService[bool]
}

// defaultConsumerKnobs are a consumer's knobs until an option says otherwise.
func defaultConsumerKnobs() consumerKnobs {
	return consumerKnobs{drain: defaultDrain, settle: defaultSettle, lease: defaultLease, autoExtend: true}
}

// declare declares the consumer n's settings in svc.
func (k *consumerKnobs) declare(svc *Service, n *nodeBase) {
	k.drainFor = declarePolicySetting(svc, n, knobDrain, k.drain, checkWait())
	k.settleFor = declarePolicySetting(svc, n, knobSettle, k.settle, checkPositive())
	k.leaseFor = declarePolicySetting(svc, n, knobLease, k.lease, checkPositive())
	k.autoExtendFor = declarePolicySetting(svc, n, knobAutoExtend, k.autoExtend, nil)
}

// consumerRun is what a consumer runs with in the app that runs it — its
// defaults before.
type consumerRun struct {
	drain, settle, lease time.Duration
	autoExtend           bool
}

// values are the knobs' values in the app that runs them.
func (k *consumerKnobs) values() consumerRun {
	return consumerRun{
		drain: knobValue(k.drainFor, k.drain), settle: knobValue(k.settleFor, k.settle),
		lease: knobValue(k.leaseFor, k.lease), autoExtend: knobValue(k.autoExtendFor, k.autoExtend),
	}
}

// policy is the queue policy of a consumer's queue: consumerPolicy, its
// lease the consumer's.
func (r consumerRun) policy(maxDeliveries int) plug.QueuePolicyValue {
	p := consumerPolicy(maxDeliveries)
	p.VisibilityTimeout = r.lease
	return p
}

// consume is cfg with the consumer's stop and lease renewal, and the delay
// a handler asks to be retried after ([RetryIn]).
func (r consumerRun) consume(cfg plug.ConsumeConfig) plug.ConsumeConfig {
	cfg.DrainTimeout, cfg.SettleTimeout, cfg.AutoExtend = r.drain, r.settle, r.autoExtend
	cfg.RetryIn = retryInOf
	return cfg
}

// stopTakes is the most the consumer's stop takes: its drain, then one
// outcome's settle.
func (r consumerRun) stopTakes() time.Duration { return r.drain + r.settle }

// config is the consumer's knobs as a mechanic's configuration shows them.
func (r consumerRun) config(into map[string]string) {
	into["drain"], into["settle"], into["lease"] = r.drain.String(), r.settle.String(), r.lease.String()
	into["autoExtend"] = strconv.FormatBool(r.autoExtend)
}

// drainer is a consumer the app tells of its stop before its component
// stops: a subscription, a watch, a queued command.
type drainer interface {
	// stopTakes is the most its stop takes, 0 for none: a command that is
	// not queued.
	stopTakes() time.Duration
	// beginDrain tells its consumer the app is stopping, without waiting,
	// and answers why it could not.
	beginDrain() error
}

// componentStopBudget is the budget the app's lifecycle gives each
// component's stop: defaultStopBudget, or the longest stop a consumer of the
// app takes — its drain plus its settle — and the margin, when that is
// longer. A CLI run starts no consumer.
func (a *App) componentStopBudget() time.Duration {
	budget := defaultStopBudget
	if a.runProfile() == model.ProfileCLI {
		return budget
	}
	for _, d := range a.drainers() {
		budget = max(budget, d.stopTakes()+stopMargin)
	}
	return budget
}

// stopDeadline is how long a whole stop of the app may take, base when no
// consumer asks for a longer component budget: base, plus what the
// consumers' budget adds to the default one.
func (a *App) stopDeadline(base time.Duration) time.Duration {
	return base + max(a.componentStopBudget()-defaultStopBudget, 0)
}

// drainers are the app's consumers.
func (a *App) drainers() []drainer {
	g := a.componentNodes()
	var out []drainer
	for _, group := range [][]node{g.subs, g.commands} {
		for _, n := range group {
			if d, ok := n.(drainer); ok {
				out = append(out, d)
			}
		}
	}
	return out
}

// beginDrains tells every consumer of the app that it is stopping, at once:
// each one stops leasing and lets its running handler finish within its
// drain, while the components before it stop. Its component's stop then
// waits for it.
func (a *App) beginDrains() {
	for _, d := range a.drainers() {
		if err := d.beginDrain(); err != nil {
			logger.Warn(context.Background(), a.log, "a consumer was not told of the stop at once: its component's stop tells it",
				logger.String("error", errs.PublicOf(err)))
		}
	}
}

// beginStop cancels the run sup supervises without waiting for it to end —
// the component's stop waits —; nothing for a consumer that never started.
// A Stop whose context is already done cancels the run and answers
// StopTimeout at once, which is what is asked of it here: any other answer
// is returned.
func beginStop(sup *lifecycle.Supervisor) error {
	if sup == nil {
		return nil
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Time{})
	defer cancel()
	if err := sup.Stop(ctx); err != nil && !errors.Is(err, lifecycle.StopTimeout) {
		return err
	}
	return nil
}

// stoppedBy reports whether err, a delivery's handler's, is its context
// ending because its consumer stops — the drain over, the app stopping —:
// the message is handed back uncounted, and it is no failure.
func stoppedBy(ctx context.Context, a *App, err error) bool {
	cancelled := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	if ctx.Err() == nil || !cancelled {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopping
}

// ended ends a delivery's span sp, begun at started, and reports the run to
// the loop: a handler the stop's drain cut short is no failure — its span
// says it was handed back, and the loop counts no error (stoppedBy).
func (l *loopState) ended(ctx context.Context, a *App, sp *span, started time.Time, err error) {
	failed := err
	if stoppedBy(ctx, a, err) {
		failed = nil
		sp.attr("handed-back", "stop")
	}
	sp.end(failed)
	l.ran(a, started, a.clock.Now(), failed)
}
