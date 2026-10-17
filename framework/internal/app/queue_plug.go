package app

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// kit's queues through the queue subsystem (ADR 0179 §1, step 4): the core
// opens every consumer's queue — a subscription's, a watch's, a queued
// command's, a mailer's outbox on a database — with a plug.QueueConfig and
// runs its consumer through the plug.Broker framework/app/queue plugs in;
// it imports none of the SDK's queue.

// queuePackage is the package a program imports for its topics,
// subscriptions, watches, queued commands and outboxes on a database.
const queuePackage = "github.com/kitsunium/sdk/framework/app/queue"

// openQueueWith opens the queue cfg places, through the queue subsystem:
// refused when the program does not import it — which the start says first,
// for every declaration at once (subsystem.go).
func (a *App) openQueueWith(cfg plug.QueueConfig) (plug.Broker, error) {
	d := plug.Queues.Load()
	if d == nil || d.Open == nil {
		return nil, queuesMissing()
	}
	cfg.Clock = pluggedClock{Timed: a.clock}
	return d.Open(cfg)
}

// deliveryHandler handles one delivery of a consumer's queue: nil
// acknowledges it, an error has it retried — or dead-lettered at once,
// through doNotRetry.
type deliveryHandler = func(ctx context.Context, d plug.DeliveryValue) error

// queuesMissing is the refusal of what needs the queue subsystem in a
// program that does not import it.
func queuesMissing() error {
	return errs.New(CodeSubsystemMissing, "SUBSYSTEM_NOT_IMPORTED", subsystemPublic,
		"app: a topic, a subscription, a watch, a queued command or an outbox on a database needs the queue subsystem, which the program does not import: "+queuePackage,
		errs.String("packages", queuePackage))
}

// queuesLinked reports whether the program imports framework/app/queue.
func queuesLinked() bool { return plug.Queues.Load() != nil }

// doNotRetry is err as a delivery's handler returns it when no retry can
// help: its queue dead-letters the message at once. It runs on a queue the
// subsystem opened, so the subsystem is there.
func doNotRetry(err error) error {
	if d := plug.Queues.Load(); d != nil && d.DoNotRetry != nil {
		return d.DoNotRetry(err)
	}
	return err
}

// notRetryable reports whether err, a delivery's handler's, says that no
// retry can help.
func notRetryable(err error) bool {
	d := plug.Queues.Load()
	return d != nil && d.NotRetryable != nil && d.NotRetryable(err)
}

// pluggedClock is the app's clock as a queue stamps and waits on it: the
// same instants and timers, handed on as plug's.
type pluggedClock struct {
	clock.Timed
}

// NewTimer is the clock's timer.
func (c pluggedClock) NewTimer(d time.Duration) plug.Timer { return c.Timed.NewTimer(d) }

// NewTicker is the clock's ticker.
func (c pluggedClock) NewTicker(d time.Duration) plug.Ticker { return c.Timed.NewTicker(d) }
