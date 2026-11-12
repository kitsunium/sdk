package app

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A streamed topic (issue #826): one stream per topic, which a publish
// appends ONE record to whatever the number of subscriptions, each
// subscription a consumer group of it with an offset of its own — where a
// topic that is not streamed puts a copy in each subscription's queue. The
// stream is the stream subsystem's (stream_plug.go).

// startKind is where a streamed topic's subscription starts, the first time
// its group meets the stream.
type startKind uint8

// The places a subscription starts at; the zero is after the last record.
const (
	startNow startKind = iota
	startBeginning
	startAt
)

// streamStart is where a subscription starts: its kind, its instant for
// startAt, and whether an option said it.
type streamStart struct {
	at   time.Time
	kind startKind
	set  bool
}

// topicStream is a streamed topic's stream: the key a message is appended
// under, and, while an app runs the topic, its stream and the database it
// lives on.
type topicStream[T any] struct {
	key func(T) string
	mu  sync.Mutex
	app *App
	s   plug.Stream
	db  *databaseRun
}

// Streamed makes the topic a stream: a publish appends the message ONCE to
// the topic's log, whatever its number of subscriptions, and each
// subscription is a consumer group that reads every record at its own pace,
// from an offset it commits — one durable write per message where a topic
// that is not streamed makes one per subscription. key chooses a message's
// partition, and the messages of one key are handled in the order they were
// published; nil, or a key that is empty, puts every message on the topic's
// own key, in one order. A handler that fails holds its partition until it
// succeeds: there are no dead letters.
//
// A subscription starts where [FromBeginning], [FromNow] — the default — or
// [FromTime] says, the first time its group meets the stream; [MaxDeliveries],
// [Lease] and [AutoExtend] are a queue's and refused. It needs
// framework/app/stream, which the start names when the program does not
// import it. Streamed is said before the topic's subscriptions are
// declared, and refuses [TopicService.Once]; [TopicService.PublishAt] is
// refused on a streamed topic.
func (t *TopicService[T]) Streamed(key func(T) string) *TopicService[T] {
	switch {
	case t.svc == nil:
	case t.log != nil:
		t.svc.problem(t.decl, t.id, "topic.streamed-twice", "topic", t.id)
	case t.once != nil:
		t.svc.problem(t.decl, t.id, "topic.once-streamed", "topic", t.id)
	case len(t.subs) > 0:
		t.svc.problem(t.decl, t.id, "topic.streamed-late", "topic", t.id)
	default:
		t.log = &topicStream[T]{key: key}
	}
	return t
}

// streamed reports whether the topic is streamed.
func (t *TopicService[T]) streamed() bool { return t.log != nil }

// streamed reports whether the subscription reads a streamed topic.
func (sb *SubscriptionWorker[T]) streamed() bool { return sb.topic != nil && sb.topic.streamed() }

// start opens a streamed topic's stream, before its subscriptions start.
func (t *TopicService[T]) start(_ context.Context, a *App) error {
	if t.log == nil {
		return nil
	}
	s, db, err := a.openTopicStream(&t.nodeBase)
	if err != nil {
		return failure(CodeQueueOpen, "STREAM_OPEN", "a streamed topic's stream cannot be opened", err, errs.String("topic", t.id))
	}
	t.log.mu.Lock()
	t.log.app, t.log.s, t.log.db = a, s, db
	t.log.mu.Unlock()
	return nil
}

// stop closes a streamed topic's stream, after its subscriptions stopped.
func (t *TopicService[T]) stop(_ context.Context, a *App) error {
	if t.log == nil {
		return nil
	}
	t.log.mu.Lock()
	s := t.log.s
	if t.log.app == a {
		t.log.app, t.log.s, t.log.db = nil, nil, nil
	} else {
		s = nil
	}
	t.log.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.Close()
}

// in is the topic's stream in a and the database it lives on, nil while a
// does not run the topic.
func (l *topicStream[T]) in(a *App) (plug.Stream, *databaseRun) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.app != a {
		return nil, nil
	}
	return l.s, l.db
}

// keyOf is the key msg is appended under on the topic node.
func (l *topicStream[T]) keyOf(node string, msg T) string {
	if l.key != nil {
		if k := l.key(msg); k != "" {
			return k
		}
	}
	return node
}

// appendStreamed appends msg to the topic's stream, once. Inside a
// transaction it is encoded now and appended at the commit — in the
// transaction, just before it commits, when the stream lives on the
// transaction's database, a refusal failing it; once it committed
// otherwise —, and a rollback drops it; the loops a publish wakes are woken
// after it.
func (t *TopicService[T]) appendStreamed(ctx context.Context, a *App, sp *span, msg T, at time.Time) error {
	if !at.IsZero() {
		return failure(CodeTopicPublish, "TOPIC_LATER_STREAMED", "a streamed topic cannot publish for later", nil, errs.String("topic", t.id))
	}
	s, db := t.log.in(a)
	if s == nil {
		return notRunning(&t.nodeBase)
	}
	payload, err := t.payload(ctx, a, msg)
	if err != nil {
		return err
	}
	key := t.log.keyOf(t.id, msg)
	appendTo := func(actx context.Context) error {
		if err := s.Append(actx, key, payload); err != nil {
			return failure(CodeTopicPublish, "TOPIC_APPEND", "the topic's stream refused the message", err, errs.String("topic", t.id))
		}
		return nil
	}
	if unitOf(ctx) == nil {
		if err := appendTo(ctx); err != nil {
			return err
		}
		t.wake()
		return nil
	}
	release := func(rctx context.Context) error { return appendTo(context.WithoutCancel(rctx)) }
	if !hold(ctx, heldEffect{node: t.id, release: release, joined: appendTo, queue: db}) {
		if err := appendTo(ctx); err != nil {
			return err
		}
	}
	wake := func(context.Context) error {
		t.wake()
		return nil
	}
	if !hold(ctx, heldEffect{node: t.id, release: wake, inside: true}) {
		t.wake()
	}
	sp.attr("held", "commit")
	return nil
}

// FromBeginning starts a streamed topic's subscription at the first message
// its stream still keeps, the first time its group meets the stream: a
// subscription added to a product that already published reads every
// message published before it. A subscription of a topic that is not
// streamed refuses it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func FromBeginning() SubscriptionConfigurer {
	return subscriptionOption(func(o *subscriptionOptions) { o.start = streamStart{kind: startBeginning, set: true} })
}

// FromNow starts a streamed topic's subscription after the last message
// published, the first time its group meets the stream: it reads what is
// published from then on, as a subscription of a topic that is not streamed
// does. It is the default. A subscription of a topic that is not streamed
// refuses it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func FromNow() SubscriptionConfigurer {
	return subscriptionOption(func(o *subscriptionOptions) { o.start = streamStart{kind: startNow, set: true} })
}

// FromTime starts a streamed topic's subscription at the first message published
// at or after at, the first time its group meets the stream; a zero instant
// is refused. A subscription of a topic that is not streamed refuses it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func FromTime(at time.Time) SubscriptionConfigurer {
	return subscriptionOption(func(o *subscriptionOptions) { o.start = streamStart{kind: startAt, at: at, set: true} })
}

// acceptsStart reports whether the options o, given to the subscription
// named name of svc, suit its topic, and says why not: a start is a
// streamed topic's, and a streamed topic's subscription takes none of a
// queue's options.
func (sb *SubscriptionWorker[T]) acceptsStart(svc *Service, o subscriptionOptions, name string) bool {
	switch {
	case !sb.streamed() && o.start.set:
		svc.problem(sb.decl, sb.id, "subscription.start-queue", "name", name)
	case sb.streamed() && len(o.given) > 0:
		svc.problem(sb.decl, sb.id, "subscription.stream-option", "name", name, "option", o.given[0])
	case o.start.kind == startAt && o.start.at.IsZero():
		svc.problem(sb.decl, sb.id, "subscription.from-zero", "name", name)
	default:
		return true
	}
	return false
}

// startStreamed runs the subscription as a consumer group of its topic's
// stream, under the SDK's supervisor as a queue's consumer is: its
// handler once per record, retried while it fails, its partition waiting.
//
// Goroutine lifecycle: the supervisor's goroutine reads the stream until
// stop cancels it and waits for it.
func (sb *SubscriptionWorker[T]) startStreamed(a *App) error {
	s, _ := sb.topic.log.in(a)
	if s == nil {
		return failure(CodeQueueOpen, "STREAM_OPEN", "a subscription's stream is not open", nil,
			errs.String("subscription", sb.id), errs.String("topic", sb.topic.id))
	}
	run := sb.knobs.values()
	loop := a.loop(sb.id+" consumer", sb.id, model.LoopConsumer, consumerHow)
	c := consumer{a: a, loop: loop, node: sb.id, name: sb.name, wake: model.WakeTopic}
	cfg := plug.StreamSubscribeConfig{
		Group:         sb.id,
		Handler:       sb.deliverRecord(&c),
		PollInterval:  pollInterval,
		DrainTimeout:  run.drain,
		SettleTimeout: run.settle,
		Parallelism:   sb.parallelism,
		FromBeginning: sb.from.kind == startBeginning,
	}
	if sb.from.kind == startAt {
		cfg.From = sb.from.at
	}
	// Joined now, before the app serves: a group that starts after the last
	// message misses none published once the start returned.
	if err := s.Join(a.baseCtx, cfg); err != nil {
		return failure(CodeQueueOpen, "STREAM_OPEN", "a subscription cannot join its topic's stream", err,
			errs.String("subscription", sb.id), errs.String("topic", sb.topic.id))
	}
	var err error
	sb.sup, err = c.supervise(func(ctx context.Context) error { return s.Subscribe(ctx, cfg) }, run.giveUp)
	if err != nil {
		return failure(CodeLoopStart, "LOOP_START", "a subscription's consumer cannot start", err, errs.String("subscription", sb.id))
	}
	return nil
}

// deliverRecord is the stream's handler: the subscription's handler in the
// publisher's trace, as a queue's delivery runs it. A record that does not
// decode is passed over, said as a problem: no retry makes it decode, and a
// stream has no dead letters to keep it in.
func (sb *SubscriptionWorker[T]) deliverRecord(c *consumer) func(context.Context, plug.StreamRecordValue) error {
	a := c.a
	return func(ctx context.Context, r plug.StreamRecordValue) error {
		id := strconv.Itoa(r.Partition) + ":" + strconv.FormatInt(r.Offset, 10)
		msg, tr, decodeErr, openErr := sb.topic.decode(ctx, a, r.Payload)
		if decodeErr != nil {
			a.problem(sb.id, say("subscription.record-undecodable", "node", sb.id, "record", id))
			return nil
		}
		d := new(plug.DeliveryValue{Message: plug.MessageValue{ID: id, Payload: r.Payload, EnqueuedAt: r.AppendedAt}, Deliveries: 1})
		return c.deliver(ctx, d, origin{trace: tr, node: sb.topic.id}, func(ctx context.Context, sp *span) error {
			if openErr != nil {
				return openErr
			}
			if sp.detailed() {
				sp.request(msg)
			}
			return sb.handler(ctx, msg)
		})
	}
}
