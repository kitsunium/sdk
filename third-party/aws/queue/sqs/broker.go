package sqs

import (
	"context"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// sqsBroker is the broker New returns: one source queue, its dead-letter
// queue, and the leases this broker handed out.
type sqsBroker struct {
	api        queueAPI
	clock      clock.Clock
	leases     *leaseTable
	queueURL   string
	deadURL    string
	policy     corequeue.PolicyValue
	maxPayload int
	visibility int32
	wait       int32
}

// New returns a broker over the SQS queue at cfg.QueueURL. It sends no
// request: everything decidable without SQS is decided here, and a refusal
// is SQSMisconfigured or core/data/queue.QueueMisconfigured.
func New(cfg Config) (broker corequeue.Broker, err error) {
	//: everything decidable without SQS is decided here.
	if verr := cfg.validate(); verr != nil {
		//: nothing built.
		return nil, verr
	}
	//: the client is lazy: no request yet.
	return newBroker(cfg, newClient(&cfg)), nil
}

// newBroker builds the broker over api; the tests hand it an in-memory queue.
func newBroker(cfg Config, api queueAPI) *sqsBroker {
	policy := cfg.Policy.Normalized()
	//: the validated configuration, the SQS numbers computed once.
	return &sqsBroker{
		api: api, clock: cfg.clockOrSystem(), leases: newLeaseTable(), queueURL: cfg.QueueURL,
		deadURL: cfg.DeadLetterQueueURL, policy: policy, maxPayload: min(policy.MaxMessageBytes, MaxPayloadBytes),
		visibility: seconds(policy.VisibilityTimeout), wait: seconds(cfg.WaitTime),
	}
}

// seconds is d in whole seconds, rounded UP: SQS counts in seconds, and a
// deadline it keeps must never fall before the one the broker hands out.
func seconds(d time.Duration) int32 {
	//: a non-positive duration is zero seconds.
	if d <= 0 {
		return 0
	}
	//: rounded up.
	return int32((d + time.Second - 1) / time.Second)
}

// Publish sends payload: one SendMessage.
func (b *sqsBroker) Publish(ctx context.Context, payload []byte) (corequeue.MessageValue, error) {
	//: no delay: receivable at once.
	return b.send(ctx, payload, 0)
}

// send is one SendMessage of payload, receivable delay seconds later.
func (b *sqsBroker) send(ctx context.Context, payload []byte, delay int32) (corequeue.MessageValue, error) {
	//: the policy's bound, and SQS's.
	if len(payload) > b.maxPayload {
		//: refused at the call site that built it.
		return corequeue.MessageValue{}, errs.Wrap(corequeue.MessageTooLarge, errs.WrapParams{},
			errs.Int("bytes", len(payload)), errs.Int("max", b.maxPayload))
	}
	now := b.clock.Now()
	out, serr := b.api.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl: aws.String(b.queueURL), MessageBody: aws.String(encodeBody(payload)),
		DelaySeconds: delay, MessageAttributes: publishAttrs(now),
	})
	//: nothing published.
	if serr != nil {
		//: SEND_FAILED.
		return corequeue.MessageValue{}, requestFailed(serr, SendFailed.Params(), "SendMessage")
	}
	//: durable: SQS stored it before it answered.
	return corequeue.MessageValue{ID: aws.ToString(out.MessageId), Payload: slices.Clone(payload), EnqueuedAt: now}, nil
}

// PublishAt sends payload, receivable at at: DelaySeconds, up to
// MaxPublishDelay.
func (b *sqsBroker) PublishAt(ctx context.Context, payload []byte, at time.Time) (corequeue.MessageValue, error) {
	delay := at.Sub(b.clock.Now())
	//: SQS holds a message back fifteen minutes at most.
	if delay > MaxPublishDelay {
		//: nothing published.
		return corequeue.MessageValue{}, delayTooLong("at", int64(delay), int64(MaxPublishDelay))
	}
	//: a past instant is now.
	return b.send(ctx, payload, seconds(delay))
}

// Receive leases up to max messages: one ReceiveMessage.
func (b *sqsBroker) Receive(ctx context.Context, max int) ([]corequeue.DeliveryValue, error) {
	//: a batch is positive, and SQS returns ten at most.
	if berr := checkBatch(max); berr != nil {
		//: refused before any request.
		return nil, berr
	}
	start := b.clock.Now()
	b.leases.prune(start)
	out, rerr := b.api.ReceiveMessage(ctx, b.receiveInput(b.queueURL, max, b.visibility, b.wait))
	//: nothing leased.
	if rerr != nil {
		//: RECEIVE_FAILED.
		return nil, requestFailed(rerr, ReceiveFailed.Params(), "ReceiveMessage")
	}
	//: read on the clock BEFORE the request, so the deadline handed out is
	//: never later than the one SQS keeps.
	deadline := start.Add(b.policy.VisibilityTimeout)
	deliveries := make([]corequeue.DeliveryValue, 0, len(out.Messages))
	//: each message, or its dead letter.
	for _, m := range out.Messages {
		//: a delivery past the budget is a message whose leases all lapsed.
		if delivery, ok := b.lease(ctx, m, deadline); ok {
			deliveries = append(deliveries, delivery)
		}
	}
	//: possibly empty: Receive does not wait past WaitTime.
	return deliveries, nil
}

// lease hands m out under deadline, or dead-letters it when it has already
// been delivered MaxDeliveries times without anyone acknowledging it.
func (b *sqsBroker) lease(ctx context.Context, m types.Message, deadline time.Time) (corequeue.DeliveryValue, bool) {
	count := receiveCount(m)
	message := decodeMessage(m)
	//: the budget was spent by consumers that died.
	if count > b.policy.MaxDeliveries {
		lapsed := &leaseValue{handle: aws.ToString(m.ReceiptHandle), message: message, deliveries: count - 1}
		//: a failure leaves it in the queue; the next receive retries.
		_ = b.deadLetter(ctx, lapsed, causeValue{reason: reasonLeaseExpired})
		//: not handed out.
		return corequeue.DeliveryValue{}, false
	}
	lease := &leaseValue{deadline: deadline, handle: aws.ToString(m.ReceiptHandle), message: message, deliveries: count}
	receipt := b.leases.grant(lease)
	//: leased.
	return corequeue.DeliveryValue{
		Lease: corequeue.LeaseValue{ExpiresAt: deadline, Receipt: receipt}, Message: message, Deliveries: count,
	}, true
}

// receiveInput is the ReceiveMessage of max messages from url.
func (b *sqsBroker) receiveInput(url string, max int, visibility, wait int32) *awssqs.ReceiveMessageInput {
	//: every attribute the broker reads back.
	return &awssqs.ReceiveMessageInput{
		QueueUrl: aws.String(url), MaxNumberOfMessages: int32(max), VisibilityTimeout: visibility,
		WaitTimeSeconds: wait, MessageAttributeNames: []string{attrAll},
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameApproximateReceiveCount, types.MessageSystemAttributeNameSentTimestamp,
		},
	}
}

// checkBatch refuses a batch no ReceiveMessage can serve.
func checkBatch(max int) error {
	//: positive, as every broker requires.
	if max <= 0 {
		//: INVALID_BATCH_SIZE.
		return errs.Wrap(corequeue.InvalidBatchSize, errs.WrapParams{}, errs.Int("max", max))
	}
	//: SQS's own ceiling, refused rather than clamped.
	if max > MaxReceiveBatch {
		//: BATCH_TOO_LARGE.
		return errs.Wrap(BatchTooLarge, errs.WrapParams{}, errs.Int("max", max))
	}
	//: servable.
	return nil
}
