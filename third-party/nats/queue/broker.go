package queue

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// headerEnqueued carries the enqueue instant, Unix nanoseconds on the
// broker's clock, on every message the broker publishes.
const headerEnqueued string = "Kitsunium-Enqueued"

// decimal is the base every number header is written in.
const decimal int = 10

// jetStreamBroker is the broker NewJetStream returns: the stream and its
// durable consumer, looked up — or created — on first use.
type jetStreamBroker struct {
	js       jetstream.JetStream
	clock    clock.Clock
	stream   jetstream.Stream
	consumer jetstream.Consumer
	leases   *leaseTable
	waker    *waker
	cfg      Config
	lease    time.Duration
	window   time.Duration
	mu       sync.Mutex
}

// NewJetStream returns a broker over cfg.Stream and its durable consumer
// cfg.Consumer. It sends no request: the stream and the consumer are looked
// up, or created when cfg.CreateStream asks, on first use. A policy the
// broker cannot honour is core/data/queue.QueueMisconfigured; a missing
// connection or name is JetStreamMisconfigured.
func NewJetStream(cfg Config) (broker corequeue.Broker, err error) {
	//: everything decidable without the server is decided here.
	if verr := cfg.validate(); verr != nil {
		//: nothing built.
		return nil, verr
	}
	js, jerr := jetstream.New(cfg.Conn)
	//: the JetStream context is local: it sends nothing.
	if jerr != nil {
		//: REQUEST_FAILED.
		return nil, requestError(jerr, "jetstream.New")
	}
	cfg.Policy = cfg.Policy.Normalized()
	//: the stream and the consumer are reached on first use.
	return &jetStreamBroker{js: js, clock: cfg.clockOrSystem(), leases: newLeaseTable(), waker: newWaker(), cfg: cfg}, nil
}

// call bounds a request by ctx and by Config.Timeout.
func (b *jetStreamBroker) call(ctx context.Context) (context.Context, context.CancelFunc) {
	//: whichever ends first.
	return context.WithTimeout(ctx, b.cfg.Timeout)
}

// ready returns the stream and the consumer, looking them up — or creating
// them — the first time.
func (b *jetStreamBroker) ready(ctx context.Context) (jetstream.Consumer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: found already.
	if b.consumer != nil {
		return b.consumer, nil
	}
	ctx, cancel := b.call(ctx)
	defer cancel()
	stream, serr := b.findStream(ctx)
	//: STREAM_MISSING or the request's failure.
	if serr != nil {
		return nil, serr
	}
	consumer, cerr := b.findConsumer(ctx, stream)
	//: CONSUMER_MISSING or the request's failure.
	if cerr != nil {
		return nil, cerr
	}
	b.stream, b.consumer = stream, consumer
	b.window = stream.CachedInfo().Config.Duplicates
	b.lease = b.cfg.Policy.VisibilityTimeout
	//: a lease is never longer than the server's AckWait, when it reports one.
	if ackWait := consumer.CachedInfo().Config.AckWait; ackWait > 0 {
		b.lease = min(b.lease, ackWait)
	}
	//: found.
	return consumer, nil
}

// findStream looks the stream up, and creates it over both subjects when
// Config.CreateStream asks: a work queue, so an acknowledged message leaves.
func (b *jetStreamBroker) findStream(ctx context.Context) (jetstream.Stream, error) {
	stream, err := b.js.Stream(ctx, b.cfg.Stream)
	//: found, or not to be created.
	if err == nil || !errors.Is(err, jetstream.ErrStreamNotFound) || !b.cfg.CreateStream {
		//: typed on failure.
		return stream, typedOrNil(err, "stream")
	}
	stream, err = b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: b.cfg.Stream, Subjects: []string{b.cfg.Subject, b.cfg.DeadLetterSubject},
		Retention: jetstream.WorkQueuePolicy, Duplicates: DefaultDuplicateWindow,
	})
	//: created, or the request's failure.
	return stream, typedOrNil(err, "create stream")
}

// findConsumer looks the durable consumer up, and creates it when
// Config.CreateStream asks: explicit acknowledgement, AckWait the policy's
// visibility timeout, and no delivery limit — the broker dead-letters.
func (b *jetStreamBroker) findConsumer(ctx context.Context, stream jetstream.Stream) (jetstream.Consumer, error) {
	consumer, err := stream.Consumer(ctx, b.cfg.Consumer)
	//: found, or not to be created.
	if err == nil || !errors.Is(err, jetstream.ErrConsumerNotFound) || !b.cfg.CreateStream {
		//: typed on failure.
		return consumer, typedOrNil(err, "consumer")
	}
	consumer, err = stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable: b.cfg.Consumer, AckPolicy: jetstream.AckExplicitPolicy, AckWait: b.cfg.Policy.VisibilityTimeout,
		MaxDeliver: -1, FilterSubject: b.cfg.Subject, DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	//: created, or the request's failure.
	return consumer, typedOrNil(err, "create consumer")
}

// typedOrNil is nil for nil and requestError otherwise.
func typedOrNil(err error, op string) error {
	//: no failure.
	if err == nil {
		return nil
	}
	//: typed.
	return requestError(err, op)
}

// Publish publishes payload on the subject and waits for the stream's
// PubAck: the stream sequence is the message's ID.
func (b *jetStreamBroker) Publish(ctx context.Context, payload []byte) (corequeue.MessageValue, error) {
	message, _, err := b.publish(ctx, payload, "")
	//: published, or not.
	return message, err
}

// publish is Publish, under a Nats-Msg-Id when msgID is not empty.
func (b *jetStreamBroker) publish(ctx context.Context, payload []byte, msgID string) (corequeue.MessageValue, bool, error) {
	//: the policy's bound.
	if len(payload) > b.cfg.Policy.MaxMessageBytes {
		//: refused at the call site that built it.
		return corequeue.MessageValue{}, false, errs.Wrap(corequeue.MessageTooLarge, errs.WrapParams{},
			errs.Int("bytes", len(payload)), errs.Int("max", b.cfg.Policy.MaxMessageBytes))
	}
	now := b.clock.Now()
	msg := nats.NewMsg(b.cfg.Subject)
	msg.Data = payload
	msg.Header.Set(headerEnqueued, strconv.FormatInt(now.UnixNano(), decimal))
	opts := []jetstream.PublishOpt{jetstream.WithExpectStream(b.cfg.Stream)}
	//: the deduplication key, when there is one.
	if msgID != "" {
		opts = append(opts, jetstream.WithMsgID(msgID))
	}
	ctx, cancel := b.call(ctx)
	defer cancel()
	ack, perr := b.js.PublishMsg(ctx, msg, opts...)
	//: no PubAck: stored or not, nobody knows.
	if perr != nil {
		//: PUBLISH_UNACKED or REQUEST_FAILED.
		return corequeue.MessageValue{}, false, publishError(perr)
	}
	//: a duplicate publishes nothing.
	if ack.Duplicate {
		return corequeue.MessageValue{}, false, nil
	}
	b.waker.signal(now)
	//: stored by the stream.
	return corequeue.MessageValue{
		ID: strconv.FormatUint(ack.Sequence, decimal), Payload: slices.Clone(payload), EnqueuedAt: now,
	}, true, nil
}

// PublishOnce publishes payload under Nats-Msg-Id key: the stream drops a
// second publication within its duplicate window. The window is the
// stream's, so window must equal it.
func (b *jetStreamBroker) PublishOnce(ctx context.Context, key string, payload []byte, window time.Duration) (corequeue.MessageValue, bool, error) {
	//: an empty key deduplicates nothing.
	if key == "" {
		return corequeue.MessageValue{}, false, errs.Wrap(corequeue.QueueMisconfigured, errs.WrapParams{}, errs.String("field", "key"))
	}
	//: the window is looked up with the stream.
	if _, rerr := b.ready(ctx); rerr != nil {
		return corequeue.MessageValue{}, false, rerr
	}
	//: JetStream deduplicates over the stream's window and no other.
	if window <= 0 || window != b.window {
		return corequeue.MessageValue{}, false, errs.Wrap(corequeue.QueueMisconfigured, errs.WrapParams{},
			errs.String("field", "window"), errs.Int64("value_ns", int64(window)), errs.Int64("stream_ns", int64(b.window)))
	}
	//: published, or recognised as a duplicate.
	return b.publish(ctx, payload, key)
}

// Receive leases up to max messages: one Fetch that waits for none.
func (b *jetStreamBroker) Receive(ctx context.Context, max int) ([]corequeue.DeliveryValue, error) {
	//: a batch is positive.
	if max <= 0 {
		//: INVALID_BATCH_SIZE.
		return nil, errs.Wrap(corequeue.InvalidBatchSize, errs.WrapParams{}, errs.Int("max", max))
	}
	consumer, rerr := b.ready(ctx)
	//: the stream or the consumer is missing.
	if rerr != nil {
		return nil, rerr
	}
	start := b.clock.Now()
	b.leases.prune(start)
	batch, ferr := consumer.FetchNoWait(max)
	//: nothing leased.
	if ferr != nil {
		//: typed.
		return nil, requestError(ferr, "fetch")
	}
	//: read on the clock BEFORE the fetch, so the deadline handed out is
	//: never later than the server's AckWait.
	deadline := start.Add(b.lease)
	deliveries := make([]corequeue.DeliveryValue, 0, max)
	//: each message, or its dead letter.
	for msg := range batch.Messages() {
		//: a delivery past the budget is a message whose leases all lapsed.
		if delivery, ok := b.grant(ctx, msg, deadline); ok {
			deliveries = append(deliveries, delivery)
		}
	}
	//: a fetch cut short keeps what it delivered: those leases hold.
	if berr := batch.Error(); berr != nil && !errors.Is(berr, jetstream.ErrNoMessages) && len(deliveries) == 0 {
		//: typed.
		return nil, requestError(berr, "fetch")
	}
	b.waker.schedule(deadline, len(deliveries))
	//: possibly empty: Receive does not wait.
	return deliveries, nil
}

// grant hands msg out under deadline, or dead-letters it when it has already
// been delivered MaxDeliveries times without anyone acknowledging it.
func (b *jetStreamBroker) grant(ctx context.Context, msg jetstream.Msg, deadline time.Time) (corequeue.DeliveryValue, bool) {
	meta, merr := msg.Metadata()
	//: a message the consumer cannot describe is left to redelivery.
	if merr != nil {
		return corequeue.DeliveryValue{}, false
	}
	count := int(meta.NumDelivered)
	message := decodeMessage(msg, meta)
	//: the budget was spent by consumers that died.
	if count > b.cfg.Policy.MaxDeliveries {
		lapsed := &leaseValue{msg: msg, message: message, deliveries: count - 1}
		//: a failure leaves it to the next redelivery.
		_ = b.deadLetter(ctx, lapsed, causeValue{reason: reasonLeaseExpired})
		//: not handed out.
		return corequeue.DeliveryValue{}, false
	}
	receipt := b.leases.grant(&leaseValue{deadline: deadline, msg: msg, message: message, deliveries: count})
	//: leased.
	return corequeue.DeliveryValue{
		Lease: corequeue.LeaseValue{ExpiresAt: deadline, Receipt: receipt}, Message: message, Deliveries: count,
	}, true
}

// decodeMessage reads the message the broker published from a delivery.
func decodeMessage(msg jetstream.Msg, meta *jetstream.MsgMetadata) corequeue.MessageValue {
	enqueued := meta.Timestamp
	//: the broker's own instant, when the publisher was a broker.
	if nanos, perr := strconv.ParseInt(msg.Headers().Get(headerEnqueued), decimal, 64); perr == nil {
		enqueued = time.Unix(0, nanos)
	}
	//: the stream sequence is the identifier.
	return corequeue.MessageValue{
		ID: strconv.FormatUint(meta.Sequence.Stream, decimal), Payload: bytes.Clone(msg.Data()), EnqueuedAt: enqueued,
	}
}
