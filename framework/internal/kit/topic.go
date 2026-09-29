// Package kit — topics and subscriptions: asynchronous messages of one type.
package kit

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"sync"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/queue"
	"github.com/kitsunium/sdk/pkg/v1/trace"
)

// TopicService is an asynchronous channel of messages of type T. Every subscription
// receives every message, at least once: a subscription's handler must
// tolerate a redelivery.
type TopicService[T any] struct {
	nodeBase
	mu   sync.Mutex
	subs []*SubscriptionWorker[T]
	// wakers are the declared loops woken by a publish (loop.go).
	wakers map[int]func()
	nextID int
}

// Topic declares a topic carrying messages of type T.
//
//go:noinline
func (s *Service) Topic[T any](name string) *TopicService[T] {
	t := NewTopicService[T]()
	t.kind, t.name, t.decl = model.KindTopic, name, callerPos()
	s.add(t, true)
	return t
}

// envelope is what travels through a subscription's queue: the message, and
// the trace context of the publish so the delivery continues the same trace
// across the asynchronous hop.
type envelope[T any] struct {
	Trace string `json:"trace,omitempty"`
	Data  T      `json:"data"`
}

// Publish sends msg to every subscription. It returns once every
// subscription's queue has accepted the message; delivery happens later.
func (t *TopicService[T]) Publish(ctx context.Context, msg T) error {
	a := t.app()
	if a == nil {
		return notRunning(&t.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: t.id, from: currentNode(ctx), edge: model.EdgePublishes, op: model.OpPublish, name: "Publish"})
	err := t.publish(ctx, a, msg)
	sp.end(err)
	t.wake()
	return err
}

// publish sends msg, with the caller's trace, to every subscription's queue.
func (t *TopicService[T]) publish(ctx context.Context, a *App, msg T) error {
	header, _ := trace.FormatTraceParent(trace.SpanContextFromContext(ctx))
	payload, err := json.Marshal(envelope[T]{Trace: header, Data: msg})
	if err != nil {
		return failure(CodeTopicEncode, "TOPIC_ENCODE", "the message cannot be encoded", err, errs.String("topic", t.id))
	}
	t.mu.Lock()
	subs := slices.Clone(t.subs)
	t.mu.Unlock()
	var failed []error
	for _, sub := range subs {
		if sub.app() != a || sub.broker == nil {
			continue
		}
		if _, err := sub.broker.Publish(ctx, payload); err != nil {
			failed = append(failed, failure(CodeTopicPublish, "TOPIC_PUBLISH", "a subscription's queue refused the message", err,
				errs.String("topic", t.id), errs.String("subscription", sub.id)))
		}
	}
	return errors.Join(failed...)
}

// describe fills the graph node out with what the Topic declares, and returns
// its edges.
func (t *TopicService[T]) describe(a *App, out *model.Node) []model.Edge {
	broker := "memory"
	if a != nil && a.data != nil {
		broker = "file"
	}
	out.Topic = &model.TopicInfo{Message: schemaOf(reflect.TypeFor[T]()), Delivery: "at-least-once", Broker: broker}
	t.mu.Lock()
	defer t.mu.Unlock()
	edges := make([]model.Edge, 0, len(t.subs))
	for _, sub := range t.subs {
		if a != nil && sub.svc.app.Load() != nil && sub.svc.app.Load() != a {
			continue
		}
		edges = append(edges, model.Edge{From: t.id, To: sub.id, Kind: model.EdgeDelivers, Declared: true})
	}
	return edges
}

// SubscriptionWorker consumes a topic: its handler runs once per message, at least
// once, retried on error, and dead-lettered after MaxDeliveries attempts.
type SubscriptionWorker[T any] struct {
	nodeBase
	topic       *TopicService[T]
	handler     func(context.Context, T) error
	maxDeliver  int
	parallelism int

	broker queue.Broker
	cancel context.CancelFunc
	done   chan struct{}
}

// SubscriptionConfigurer configures a subscription.
type SubscriptionConfigurer interface {
	subscriptionConfigure(o *subscriptionOptions)
}

type subscriptionOptions struct {
	deliveryOptions
}

// deliveryOptions are how a queue delivers: a subscription's, or a queued
// command's.
type deliveryOptions struct {
	maxDeliveries int
	parallelism   int
}

// DeliveryOption configures a queue's deliveries: a subscription's, or a
// queued command's ([MaxDeliveries], [Parallelism]).
type DeliveryOption interface {
	SubscriptionConfigurer
	CommandConfigurer
}

// deliveryOption is a DeliveryOption.
type deliveryOption func(o *deliveryOptions)

// subscriptionConfigure sets the option on what it configures.
func (f deliveryOption) subscriptionConfigure(o *subscriptionOptions) { f(&o.deliveryOptions) }

// commandConfigure sets the delivery option on a queued command.
func (f deliveryOption) commandConfigure(o *commandOptions) {
	o.deliveriesGiven = true
	f(&o.deliveries)
}

// MaxDeliveries is how many attempts a message gets before it is
// dead-lettered — a subscription's, or a queued command's. The default is
// 5.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func MaxDeliveries(n int) DeliveryOption {
	return deliveryOption(func(o *deliveryOptions) { o.maxDeliveries = n })
}

// Parallelism is how many messages are handled at once — a subscription's,
// or a queued command's. The default is 1.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Parallelism(n int) DeliveryOption {
	return deliveryOption(func(o *deliveryOptions) { o.parallelism = n })
}

// Subscribe declares a subscription of the service to topic. Delivery is at
// least once: handler must be idempotent, which is the one thing the SDK's
// queue cannot check and kit therefore asks you to promise here.
//
//go:noinline
func (s *Service) Subscribe[T any](name string, topic *TopicService[T], handler func(context.Context, T) error, opts ...SubscriptionConfigurer) *SubscriptionWorker[T] {
	o := subscriptionOptions{deliveryOptions{maxDeliveries: defaultMaxDeliveries, parallelism: 1}}
	for _, opt := range opts {
		opt.subscriptionConfigure(&o)
	}
	sub := &SubscriptionWorker[T]{topic: topic, handler: handler, maxDeliver: o.maxDeliveries, parallelism: o.parallelism}
	sub.kind, sub.name, sub.decl = model.KindSubscription, name, callerPos()
	if p, _ := funcInfo(handler); p.file != "" {
		sub.body = &p
	}
	s.add(sub, true)
	switch {
	case topic == nil:
		s.problem(sub.decl, sub.id, "subscription.nil-topic", "name", name)
	case handler == nil:
		s.problem(sub.decl, sub.id, "subscription.nil-handler", "name", name)
	case o.maxDeliveries < 1 || o.parallelism < 1:
		s.problem(sub.decl, sub.id, "subscription.limits", "name", name)
	default:
		topic.mu.Lock()
		topic.subs = append(topic.subs, sub)
		topic.mu.Unlock()
	}
	return sub
}

// start consumes the subscription's queue until stop.
//
// Goroutine lifecycle: one goroutine consumes the subscription's queue until
// stop cancels its context; it closes sb.done, which stop waits for.
func (sb *SubscriptionWorker[T]) start(_ context.Context, a *App) error {
	if sb.topic.svc.app.Load() != a {
		return failure(CodeNotMounted, "TOPIC_NOT_MOUNTED", "a subscription consumes a topic whose service is not mounted in this app", nil,
			errs.String("subscription", sb.id), errs.String("topic", sb.topic.id))
	}
	var err error
	sb.broker, err = a.openQueue(filepath.Join(sb.svc.name, "queues", sb.name), sb.maxDeliver)
	if err != nil {
		return failure(CodeQueueOpen, "QUEUE_OPEN", "a subscription's queue cannot be opened", err, errs.String("subscription", sb.id))
	}
	loop := a.loop(sb.id+" consumer", sb.id, model.LoopConsumer, consumerHow)
	ctx, cancel := context.WithCancel(context.WithoutCancel(a.baseCtx))
	sb.cancel, sb.done = cancel, make(chan struct{})
	go func() {
		done := sb.done
		defer close(done)
		ctx := a.asLoop(ctx, loop.Name)
		err := queue.Consume(ctx, sb.broker, queue.ConsumerConfig{
			Handler:             sb.deliver(a, loop),
			Clock:               a.clock,
			PollInterval:        pollInterval,
			Parallelism:         sb.parallelism,
			BatchSize:           consumerBatch,
			HandlerIsIdempotent: true,
		})
		if err != nil && ctx.Err() == nil {
			a.problem(sb.id, say("subscription.stopped", "subscription", sb.id, "detail", errs.PublicOf(err)))
			loop.died(a, err)
		}
	}()
	return nil
}

// deliver is the queue handler: it restores the publisher's trace context,
// runs the subscription's handler inside the subscription node, and reports
// the run to the internal loop.
func (sb *SubscriptionWorker[T]) deliver(a *App, loop *loopState) queue.Handler {
	c := consumer{a: a, loop: loop, node: sb.id, name: sb.name, wake: model.WakeTopic}
	return func(ctx context.Context, d queue.Delivery) error {
		var env envelope[T]
		decodeErr := json.Unmarshal(d.Message.Payload, &env)
		if decodeErr != nil {
			env.Trace = ""
		}
		return c.deliver(ctx, &d, origin{trace: env.Trace, node: sb.topic.id}, func(ctx context.Context, sp *span) error {
			if decodeErr != nil {
				return failure(CodeUndecodable, "MESSAGE_UNDECODABLE", "the message does not decode into the topic's type", decodeErr,
					errs.String("subscription", sb.id), errs.String("message", d.Message.ID))
			}
			if sp.detailed() {
				sp.request(env.Data)
			}
			return sb.handler(ctx, env.Data)
		})
	}
}

// stop ends the consumer and waits for it until ctx ends.
func (sb *SubscriptionWorker[T]) stop(ctx context.Context, _ *App) error {
	if sb.cancel == nil {
		return nil
	}
	sb.cancel()
	select {
	case <-sb.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// describe fills the graph node out with what the Subscription declares, and
// returns its edges.
func (sb *SubscriptionWorker[T]) describe(_ *App, out *model.Node) []model.Edge {
	out.Subscription = &model.SubscriptionInfo{MaxDeliveries: sb.maxDeliver, Parallelism: sb.parallelism}
	if sb.topic != nil {
		out.Subscription.Topic = sb.topic.id
	}
	if r, ok := sb.broker.(queue.DeadLetterReader); ok {
		if dl, err := r.DeadLetters(context.Background(), 1000); err == nil {
			out.Subscription.DeadLetters = new(len(dl))
		}
	}
	return nil
}

// NewTopicService is a topic no service declares yet: [Service.Topic] makes
// one and declares it, which is how a product gets one.
func NewTopicService[T any]() *TopicService[T] { return &TopicService[T]{} }
