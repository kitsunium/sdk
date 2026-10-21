package queueengine

import (
	"context"
	"io"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A queue as kit's core holds it (plug.Broker): one of the SDK's brokers, the
// clock its consumer waits on, and the conversions between the SDK's values
// and plug's.

// broker is one of the SDK's brokers as kit's core uses it.
type broker struct {
	q     queue.Broker
	clock clock.Timed
}

// Publish puts payload in the queue, in the transaction ctx carries for its
// database when it lives on one.
func (b *broker) Publish(ctx context.Context, payload []byte) error {
	_, err := b.q.Publish(ctx, payload)
	return err
}

// PublishAt puts payload in the queue, received by no consumer before at,
// in the transaction ctx carries for its database when it lives on one.
func (b *broker) PublishAt(ctx context.Context, payload []byte, at time.Time) error {
	deferred, ok := b.q.(queue.DelayedPublisher)
	if !ok {
		return lacks("DelayedPublisher")
	}
	_, err := deferred.PublishAt(ctx, payload, at)
	return err
}

// PublishOnce puts payload in the queue unless a publication under key was
// made within window, and reports whether it did.
func (b *broker) PublishOnce(ctx context.Context, key string, payload []byte, window time.Duration) (bool, error) {
	once, ok := b.q.(queue.OncePublisher)
	if !ok {
		return false, lacks("OncePublisher")
	}
	_, published, err := once.PublishOnce(ctx, key, payload, window)
	return published, err
}

// Consume runs the SDK's consumer over the queue until ctx ends or its
// storage fails: every handler kit runs tolerates a redelivery. A stop lets
// the running handler finish within cfg's drain, each outcome bounded by its
// settle, and the lease of a running handler is renewed when cfg says so. A
// failure whose handler asked for a delay of its own (cfg.RetryIn) is the
// SDK's queue.RetryAfter: its message comes back after that delay.
func (b *broker) Consume(ctx context.Context, cfg plug.ConsumeConfig) error {
	handle, retryIn := cfg.Handler, cfg.RetryIn
	return queue.Consume(ctx, b.q, queue.ConsumerConfig{
		Handler: func(ctx context.Context, d queue.Delivery) error {
			err := handle(ctx, plug.DeliveryValue{Message: pluggedMessage(d.Message), Deliveries: d.Deliveries})
			if retryIn == nil || err == nil {
				return err
			}
			if delay, asked := retryIn(err); asked {
				return queue.RetryAfter(err, delay)
			}
			return err
		},
		Clock:               b.clock,
		PollInterval:        cfg.PollInterval,
		Parallelism:         cfg.Parallelism,
		BatchSize:           cfg.BatchSize,
		DrainTimeout:        cfg.DrainTimeout,
		SettleTimeout:       cfg.SettleTimeout,
		AutoExtend:          cfg.AutoExtend,
		HandlerIsIdempotent: true,
	})
}

// DeadLetters are up to maxLetters of the queue's dead letters.
func (b *broker) DeadLetters(ctx context.Context, maxLetters int) ([]plug.DeadLetterValue, error) {
	r, ok := b.q.(queue.DeadLetterReader)
	if !ok {
		return nil, lacks("DeadLetterReader")
	}
	dead, err := r.DeadLetters(ctx, maxLetters)
	if err != nil {
		return nil, err
	}
	out := make([]plug.DeadLetterValue, len(dead))
	for i := range dead {
		out[i] = pluggedDeadLetter(&dead[i])
	}
	return out, nil
}

// Messages are up to maxMessages live messages after the identifier after,
// as they lie.
func (b *broker) Messages(ctx context.Context, after string, maxMessages int) ([]plug.LiveMessageValue, error) {
	r, ok := b.q.(queue.MessageReader)
	if !ok {
		return nil, lacks("MessageReader")
	}
	live, err := r.Messages(ctx, after, maxMessages)
	if err != nil {
		return nil, err
	}
	out := make([]plug.LiveMessageValue, len(live))
	for i, m := range live {
		out[i] = plug.LiveMessageValue{VisibleAt: m.VisibleAt, Message: pluggedMessage(m.Message), Deliveries: m.Deliveries, Leased: m.Leased}
	}
	return out, nil
}

// PutMessage writes live as it lies.
func (b *broker) PutMessage(ctx context.Context, live plug.LiveMessageValue) (string, error) {
	w, ok := b.q.(queue.MessageWriter)
	if !ok {
		return "", lacks("MessageWriter")
	}
	return w.PutMessage(ctx, queue.LiveMessage{VisibleAt: live.VisibleAt, Message: sdkMessage(live.Message), Deliveries: live.Deliveries, Leased: live.Leased})
}

// PutDeadLetter writes dead as it lies.
func (b *broker) PutDeadLetter(ctx context.Context, dead plug.DeadLetterValue) (string, error) {
	w, ok := b.q.(queue.DeadLetterWriter)
	if !ok {
		return "", lacks("DeadLetterWriter")
	}
	return w.PutDeadLetter(ctx, queue.DeadLetter{
		FailedAt: dead.FailedAt, Reason: dead.Reason, Cause: dead.Cause, Message: sdkMessage(dead.Message),
		Code: dead.Code, Deliveries: dead.Deliveries,
	})
}

// Close releases what the queue holds — a file queue's directory
// descriptors —; nothing for a queue that holds none.
func (b *broker) Close() error {
	if c, ok := b.q.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// BrokerOf is the SDK's broker behind b, a queue this package opened: what a
// mailer's spool keeps its outbox in on a database (ADR 0179 step 6) — the
// sealed outbox, behind a test's wrapper when it set one —; false for a
// queue it did not open.
func BrokerOf(b plug.Broker) (queue.Broker, bool) {
	opened, ok := b.(*broker)
	if !ok {
		return nil, false
	}
	return opened.q, true
}

// lacks is the refusal of a capability the SDK's broker behind a queue does
// not have.
func lacks(capability string) error {
	return errs.Wrap(queue.QueueMisconfigured, errs.WrapParams{}, errs.String("capability", capability))
}

// pluggedMessage is the SDK's message as kit's core reads it.
func pluggedMessage(m queue.Message) plug.MessageValue {
	return plug.MessageValue{EnqueuedAt: m.EnqueuedAt, ID: m.ID, Payload: m.Payload}
}

// sdkMessage is kit's message as the SDK's queue takes it.
func sdkMessage(m plug.MessageValue) queue.Message {
	return queue.Message{EnqueuedAt: m.EnqueuedAt, ID: m.ID, Payload: m.Payload}
}

// pluggedDeadLetter is the SDK's dead letter as kit's core reads it.
func pluggedDeadLetter(d *queue.DeadLetter) plug.DeadLetterValue {
	return plug.DeadLetterValue{
		FailedAt: d.FailedAt, Reason: d.Reason, Cause: d.Cause, Message: pluggedMessage(d.Message),
		Code: d.Code, Deliveries: d.Deliveries,
	}
}

// pluggedStep is the SDK's migration step s on the standard library's
// executor, nil for none.
func pluggedStep(s sql.Step) plug.Step {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, ex plug.Executor) error { return s(ctx, ex) }
}
