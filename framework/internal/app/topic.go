package app

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/lifecycle"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
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
	// once is the key a message is published once under (Once), nil for a
	// topic that publishes every message; onceFor is its window's setting.
	once       func(T) string
	onceWindow time.Duration
	onceFor    *SettingService[time.Duration]
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

// Once makes the topic publish a message at most once per key over window:
// a second [TopicService.Publish] whose message has the key of one published
// within the window publishes nothing, in no subscription's queue, and
// answers nil — a producer that retries a publication it never heard back
// from adds no copy. The window is counted by each subscription's queue,
// from its first publication under the key; it is the SDK's
// queue.PublishOnce. A message whose key is empty is refused. Delivery stays
// at least once: a handler still tolerates a redelivery.
//
// window is the default of the topic's setting
// <service>-topic-<name>-once-window, which config/<env>.* and
// <PRODUCT>_<NAME> override; one that is not positive stops the start. A
// queued command keeps its own key ([Command.Key]): a dispatch whose key
// waits or runs is not queued again, however long ago it was dispatched.
func (t *TopicService[T]) Once(key func(T) string, window time.Duration) *TopicService[T] {
	switch {
	case t.svc == nil:
	case key == nil:
		t.svc.problem(t.decl, t.id, "topic.once-nil", "topic", t.id)
	case t.once != nil:
		t.svc.problem(t.decl, t.id, "topic.once-twice", "topic", t.id)
	default:
		t.once, t.onceWindow = key, window
		t.onceFor = declarePolicySetting(t.svc, &t.nodeBase, knobOnceWindow, window, checkPositive())
	}
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
// Inside a transaction ([Transact], a command's) the message is encoded now
// and queued at the commit: in the transaction, just before it commits, for
// a subscription whose queue lives on the transaction's database — a queue
// that refuses it then fails the transaction —, once it committed for the
// others. A rollback drops it. A topic that publishes once ([TopicService.Once])
// publishes a message whose key it published within its window in no queue.
func (t *TopicService[T]) Publish(ctx context.Context, msg T) error {
	return t.publishAs(ctx, msg, time.Time{}, "Publish")
}

// PublishAt sends msg to every subscription as [TopicService.Publish] does,
// but no subscription receives it before at: a reminder, a trial that ends,
// an order cancelled if still unpaid. It is as durable as any message, held
// to the commit inside a transaction as a publication is; an instant already
// past is now. It does not recur — a job does. A topic that publishes once
// refuses it: the SDK has no publication once for later yet. It is the SDK's
// queue.PublishAt.
func (t *TopicService[T]) PublishAt(ctx context.Context, msg T, at time.Time) error {
	if at.IsZero() {
		return t.Publish(ctx, msg)
	}
	return t.publishAs(ctx, msg, at, "PublishAt")
}

// publishAs publishes msg, at the instant at — now when it is zero —, in a
// span named name.
func (t *TopicService[T]) publishAs(ctx context.Context, msg T, at time.Time, name string) error {
	a := t.app()
	if a == nil {
		return notRunning(&t.nodeBase)
	}
	ctx, sp := a.begin(ctx, &spanStart{node: t.id, from: currentNode(ctx), edge: model.EdgePublishes, op: model.OpPublish, name: name})
	pub, err := t.publication(msg, at)
	var payload []byte
	if err == nil {
		pub.mark(sp)
		payload, err = t.payload(ctx, a, msg)
	}
	if err == nil {
		err = t.send(ctx, a, sp, payload, pub)
	}
	sp.end(err)
	return err
}

// publication is how msg goes into the subscriptions' queues: at at, or
// once under its key on a topic that publishes once.
func (t *TopicService[T]) publication(msg T, at time.Time) (publication, error) {
	if t.once == nil {
		return publication{at: at}, nil
	}
	if !at.IsZero() {
		return publication{}, laterOnce(&t.nodeBase)
	}
	key := t.once(msg)
	if key == "" {
		return publication{}, noTopicKey(&t.nodeBase)
	}
	return publication{key: key, window: knobValue(t.onceFor, t.onceWindow)}, nil
}

// send puts payload in every subscription's queue as pub says, then wakes
// the loops a publish wakes — a publication for later wakes none. Inside a
// transaction each subscription's publication is held on its own, tagged
// with the database its queue lives on (ADR 0178 C1), and the wake after
// them stays in the process.
func (t *TopicService[T]) send(ctx context.Context, a *App, sp *span, payload []byte, pub publication) error {
	wakes := pub.at.IsZero()
	if unitOf(ctx) == nil {
		err := t.publish(ctx, a, sp, payload, pub)
		if wakes {
			t.wake()
		}
		return err
	}
	var failed []error
	for _, sub := range t.subscribers(a) {
		release := func(rctx context.Context) error { return t.publishTo(context.WithoutCancel(rctx), sub, payload, pub) }
		joined := func(tctx context.Context) error { return t.publishTo(tctx, sub, payload, pub) }
		if hold(ctx, heldEffect{node: t.id, release: release, joined: joined, queue: sub.queueDB}) {
			continue
		}
		if err := t.publishTo(ctx, sub, payload, pub); err != nil {
			failed = append(failed, err)
		}
	}
	wake := func(context.Context) error {
		t.wake()
		return nil
	}
	if wakes && !hold(ctx, heldEffect{node: t.id, release: wake, inside: true}) {
		t.wake()
	}
	sp.attr("held", "commit")
	return errors.Join(failed...)
}

// payload is what a subscription's queue carries of msg: the message — its
// members kit seals sealed (seal_message.go) — and the publish's trace.
func (t *TopicService[T]) payload(ctx context.Context, a *App, msg T) ([]byte, error) {
	header, _ := trace.FormatTraceParent(trace.SpanContextFromContext(ctx))
	payload, err := t.encode(ctx, a, header, msg)
	switch {
	case errs.HasCode(err, CodeSealWrite), errs.HasCode(err, CodeSealKey):
		return nil, err
	case err != nil:
		return nil, failure(CodeTopicEncode, "TOPIC_ENCODE", "the message cannot be encoded", err, errs.String("topic", t.id))
	}
	return payload, nil
}

// publish puts payload in every subscription's queue as pub says; the span
// says when a queue had it already, under its key.
func (t *TopicService[T]) publish(ctx context.Context, a *App, sp *span, payload []byte, pub publication) error {
	var failed []error
	for _, sub := range t.subscribers(a) {
		published, err := t.publishInto(ctx, sub, payload, pub)
		if err != nil {
			failed = append(failed, err)
		}
		if err == nil && !published {
			sp.attr("deduplicated", "true")
		}
	}
	return errors.Join(failed...)
}

// subscribers are the subscriptions of the topic running in a, their queue
// open.
func (t *TopicService[T]) subscribers(a *App) []*SubscriptionWorker[T] {
	t.mu.Lock()
	subs := slices.Clone(t.subs)
	t.mu.Unlock()
	return slices.DeleteFunc(subs, func(sub *SubscriptionWorker[T]) bool { return sub.app() != a || sub.broker == nil })
}

// publishTo puts payload in sub's queue as pub says.
func (t *TopicService[T]) publishTo(ctx context.Context, sub *SubscriptionWorker[T], payload []byte, pub publication) error {
	_, err := t.publishInto(ctx, sub, payload, pub)
	return err
}

// publishInto puts payload in sub's queue as pub says, and reports whether
// it did: false for a message its key put there within its window.
func (t *TopicService[T]) publishInto(ctx context.Context, sub *SubscriptionWorker[T], payload []byte, pub publication) (bool, error) {
	published, err := pub.into(ctx, sub.broker, payload)
	if err != nil {
		return false, failure(CodeTopicPublish, "TOPIC_PUBLISH", "a subscription's queue refused the message", err,
			errs.String("topic", t.id), errs.String("subscription", sub.id))
	}
	return published, nil
}

// describe fills the graph node out with what the Topic declares, and returns
// its edges.
func (t *TopicService[T]) describe(a *App, out *model.Node) []model.Edge {
	// Where a subscription of the topic's own service waits: every one, in a
	// product whose one default database keeps them all.
	broker := "memory"
	if a != nil && a.data != nil {
		broker = queueWhere(a, &t.nodeBase)
	}
	out.Topic = &model.TopicInfo{Message: keptSchemaOf(reflect.TypeFor[T](), a != nil && t.sealsAtRest(a)), Delivery: "at-least-once", Broker: broker}
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
	// knobs are its consumer's stop and lease, declared as settings of its
	// service (consumer_policy.go).
	knobs consumerKnobs

	broker plug.Broker
	// queueDB is the database its queue lives on, nil for a file or a
	// memory queue: a publication in a transaction on it joins the
	// transaction (ADR 0178 C1).
	queueDB *databaseRun
	// sup runs the subscription's consumer while the app runs: the SDK's
	// supervisor, which restarts it after its queue fails.
	sup *lifecycle.Supervisor
}

// queueUnder is what the subscription's queue is kept under.
func (sb *SubscriptionWorker[T]) queueUnder() string { return underQueues }

// SubscriptionConfigurer configures a subscription.
type SubscriptionConfigurer interface {
	subscriptionConfigure(o *subscriptionOptions)
}

type subscriptionOptions struct {
	deliveryOptions
	// ownStores lets a watch hear its own module's stores (OwnStores).
	ownStores bool
}

// subscriptionOption is a SubscriptionConfigurer that is no DeliveryOption.
type subscriptionOption func(o *subscriptionOptions)

// subscriptionConfigure applies the option to a subscription's options.
func (f subscriptionOption) subscriptionConfigure(o *subscriptionOptions) { f(o) }

// OwnStores lets a watch ([Service.Watch]) hear the stores of its own
// module too — the product's own, for a watch of the product's —, which a
// watch never hears by default. Such a watch never hears the writes its own
// handler makes, in its handler's context — a handler that writes what it
// watches, a screening that stamps the record it screened, would otherwise
// hear itself forever —; a write the handler only causes, later and
// elsewhere — a queued command's handling, a topic's delivery — is heard.
// A subscription to a topic refuses it: a topic has no stores.
func OwnStores() SubscriptionConfigurer {
	return subscriptionOption(func(o *subscriptionOptions) { o.ownStores = true })
}

// deliveryOptions are how a queue delivers: a subscription's, or a queued
// command's.
type deliveryOptions struct {
	maxDeliveries int
	parallelism   int
	// knobs are the consumer's stop and lease (consumer_policy.go).
	knobs consumerKnobs
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

// Drain is how long the message a handler is running when the app stops may
// still run before its context is cancelled — a subscription's, a watch's, or
// a queued command's. A handler that finishes within it is acknowledged; one
// it cuts short is handed back uncounted, delivered again at the next start,
// and is no failure. The default is 5 seconds; zero cancels the handler at
// once, and a negative one stops the start. The app's stop gives each
// component the longest drain plus its settle ([Settle]), and tells every
// consumer at once, so that their drains overlap.
//
// d is the default of the consumer's setting <service>-<kind>-<name>-drain,
// the kind subscription or command, which config/<env>.* and
// <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Drain(d time.Duration) DeliveryOption {
	return deliveryOption(func(o *deliveryOptions) { o.knobs.drain = d })
}

// Settle bounds each outcome a consumer sends once the app began to stop —
// an acknowledgement, a nack, a message handed back — and each renewal of a
// lease ([AutoExtend]): a queue that stopped answering cannot hold the stop.
// The default is 5 seconds; one that is not positive stops the start.
//
// d is the default of the consumer's setting <service>-<kind>-<name>-settle,
// which config/<env>.* and <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Settle(d time.Duration) DeliveryOption {
	return deliveryOption(func(o *deliveryOptions) { o.knobs.settle = d })
}

// Lease is how long a delivered message is held by its consumer before the
// queue hands it to another — a subscription's, a watch's, or a queued
// command's. The default is 30 seconds; one that is not positive stops the
// start. With [AutoExtend], a handler that runs longer keeps it.
//
// d is the default of the consumer's setting <service>-<kind>-<name>-lease,
// which config/<env>.* and <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Lease(d time.Duration) DeliveryOption {
	return deliveryOption(func(o *deliveryOptions) { o.knobs.lease = d })
}

// AutoExtend says whether the lease of the message a handler is running is
// renewed, a third of the lease after it was granted or last renewed, for as
// long as the handler runs — a subscription's, a watch's, or a queued
// command's. It is on by default: a slow handler keeps its message instead of
// seeing it delivered to another while it runs. Off, the handler is stopped a
// margin before its [Lease] ends. A renewal the queue refuses cancels the
// handler's context. A mailer's outbox never renews: the mail spool parks a
// mail by its own lease.
//
// on is the default of the consumer's setting
// <service>-<kind>-<name>-auto-extend, which config/<env>.* and
// <PRODUCT>_<NAME> override.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func AutoExtend(on bool) DeliveryOption {
	return deliveryOption(func(o *deliveryOptions) { o.knobs.autoExtend = on })
}

// Subscribe declares a subscription of the service to topic. Delivery is at
// least once: handler must be idempotent, which is the one thing the SDK's
// queue cannot check and the framework therefore asks you to promise here.
//
//go:noinline
func (s *Service) Subscribe[T any](name string, topic *TopicService[T], handler func(context.Context, T) error, opts ...SubscriptionConfigurer) *SubscriptionWorker[T] {
	o := subscriptionOptions{deliveryOptions: defaultDeliveries()}
	for _, opt := range opts {
		opt.subscriptionConfigure(&o)
	}
	sub := &SubscriptionWorker[T]{topic: topic, handler: handler, maxDeliver: o.maxDeliveries, parallelism: o.parallelism, knobs: o.knobs}
	sub.kind, sub.name, sub.decl = model.KindSubscription, name, callerPos()
	sub.body = bodyOf(handler)
	s.add(sub, true)
	sub.knobs.declare(s, &sub.nodeBase)
	switch {
	case topic == nil:
		s.problem(sub.decl, sub.id, "subscription.nil-topic", "name", name)
	case handler == nil:
		s.problem(sub.decl, sub.id, "subscription.nil-handler", "name", name)
	case o.maxDeliveries < 1 || o.parallelism < 1:
		s.problem(sub.decl, sub.id, "subscription.limits", "name", name)
	case o.ownStores:
		s.problem(sub.decl, sub.id, "subscription.own-stores", "name", name)
	default:
		topic.mu.Lock()
		topic.subs = append(topic.subs, sub)
		topic.mu.Unlock()
	}
	return sub
}

// start consumes the subscription's queue until stop, under the SDK's
// supervisor: a consumer whose queue fails comes back after kit's backoff.
//
// Goroutine lifecycle: the supervisor's goroutine consumes the
// subscription's queue until stop cancels it and waits for it.
func (sb *SubscriptionWorker[T]) start(_ context.Context, a *App) error {
	if sb.topic.svc.app.Load() != a {
		return failure(CodeNotMounted, "TOPIC_NOT_MOUNTED", "a subscription consumes a topic whose service is not mounted in this app", nil,
			errs.String("subscription", sb.id), errs.String("topic", sb.topic.id))
	}
	var err error
	run := sb.knobs.values()
	sb.broker, sb.queueDB, err = a.openConsumerQueue(&sb.nodeBase, sb.queueUnder(), run.policy(sb.maxDeliver))
	if err != nil {
		return failure(CodeQueueOpen, "QUEUE_OPEN", "a subscription's queue cannot be opened", err, errs.String("subscription", sb.id))
	}
	loop := a.loop(sb.id+" consumer", sb.id, model.LoopConsumer, consumerHow)
	c := consumer{a: a, loop: loop, node: sb.id, name: sb.name, wake: model.WakeTopic}
	broker, handler := sb.broker, sb.deliver(&c)
	sb.sup, err = c.supervise(func(ctx context.Context) error {
		return broker.Consume(ctx, run.consume(plug.ConsumeConfig{
			Handler:      handler,
			PollInterval: pollInterval,
			Parallelism:  sb.parallelism,
			BatchSize:    consumerBatch,
		}))
	}, run.giveUp)
	if err != nil {
		return failure(CodeLoopStart, "LOOP_START", "a subscription's consumer cannot start", err, errs.String("subscription", sb.id))
	}
	return nil
}

// deliver is the queue handler: it restores the publisher's trace context,
// runs the subscription's handler inside the subscription node, and reports
// the run to the internal loop. A message that does not decode is
// dead-lettered on its first delivery: no retry makes it decode (ADR 0151
// §D8).
func (sb *SubscriptionWorker[T]) deliver(c *consumer) func(context.Context, plug.DeliveryValue) error {
	a := c.a
	return func(ctx context.Context, d plug.DeliveryValue) error {
		msg, tr, decodeErr, openErr := sb.topic.decode(ctx, a, d.Message.Payload)
		if decodeErr != nil {
			tr = ""
		}
		return c.deliver(ctx, &d, origin{trace: tr, node: sb.topic.id}, func(ctx context.Context, sp *span) error {
			switch {
			case openErr != nil:
				return openErr
			case decodeErr != nil:
				return doNotRetry(failure(CodeUndecodable, "MESSAGE_UNDECODABLE", "the message does not decode into the topic's type", decodeErr,
					errs.String("subscription", sb.id), errs.String("message", d.Message.ID)))
			}
			if sp.detailed() {
				sp.request(msg)
			}
			return sb.handler(ctx, msg)
		})
	}
}

// stop ends the consumer and waits for it until ctx ends.
func (sb *SubscriptionWorker[T]) stop(ctx context.Context, _ *App) error {
	if sb.sup == nil {
		return nil
	}
	return sb.sup.Stop(ctx)
}

// stopTakes is the most the subscription's stop takes: its drain and its
// settle.
func (sb *SubscriptionWorker[T]) stopTakes() time.Duration { return sb.knobs.values().stopTakes() }

// beginDrain tells the subscription's consumer the app is stopping.
func (sb *SubscriptionWorker[T]) beginDrain() error { return beginStop(sb.sup) }

// describe fills the graph node out with what the Subscription declares, and
// returns its edges.
func (sb *SubscriptionWorker[T]) describe(_ *App, out *model.Node) []model.Edge {
	out.Subscription = &model.SubscriptionInfo{MaxDeliveries: sb.maxDeliver, Parallelism: sb.parallelism}
	if sb.topic != nil {
		out.Subscription.Topic = sb.topic.id
	}
	if sb.broker != nil {
		if dl, err := sb.broker.DeadLetters(context.Background(), 1000); err == nil {
			out.Subscription.DeadLetters = new(len(dl))
		}
	}
	return nil
}

// NewTopicService is a topic no service declares yet: [Service.Topic] makes
// one and declares it, which is how a product gets one.
func NewTopicService[T any]() *TopicService[T] { return &TopicService[T]{} }
