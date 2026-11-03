package sqs

import (
	"time"

	"github.com/kitsunium/sdk/internal/core/observe/logger/writer"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// MaxReceiveBatch is the most messages one Receive leases: ReceiveMessage's
// MaxNumberOfMessages ceiling. A larger max is refused with BatchTooLarge
// rather than clamped, so a consumer configured for more learns it at once.
const MaxReceiveBatch int = 10

// MaxWaitTime is the longest long poll SQS accepts (WaitTimeSeconds 20).
const MaxWaitTime time.Duration = 20 * time.Second

// MaxPublishDelay is the latest a delayed publication may become receivable
// (DelaySeconds 900).
const MaxPublishDelay time.Duration = 15 * time.Minute

// MaxVisibility is the longest lease, retry delay or renewal SQS records
// (VisibilityTimeout 43200 s). A policy whose VisibilityTimeout or retry
// ceiling is longer is refused at New.
const MaxVisibility time.Duration = 12 * time.Hour

// MaxPayloadBytes is the largest payload Publish sends: 191 KiB, which
// base64 and the broker's attributes keep under the 256 KiB an SQS
// message has carried since the service began, and every emulator
// accepts. A Policy.MaxMessageBytes above it is lowered to it, and a
// larger payload is core/data/queue.MessageTooLarge.
const MaxPayloadBytes int = 191 << 10

// Config configures New: the queue, its dead-letter queue, how to reach
// them, and the delivery discipline the broker enforces.
type Config struct {
	// QueueURL is the queue's URL, as GetQueueUrl returns it. Required.
	QueueURL string
	// DeadLetterQueueURL is the queue a message is moved to with its cause
	// once it has been delivered Policy.MaxDeliveries times, or rejected.
	// Required: the broker owns the decision to dead-letter (ADR 0054), and
	// a Nack on a last attempt with nowhere to go would lose the message.
	// The source queue's own redrive policy, if any, should allow more
	// receives than MaxDeliveries, so SQS never moves a message first.
	DeadLetterQueueURL string
	// Region is the AWS region of both queues. Required.
	Region string
	// Endpoint overrides the SQS endpoint — LocalStack, ElasticMQ, a VPC
	// endpoint. Empty means the region's.
	Endpoint string
	// Credentials signs every request, re-read on each signature so a
	// rotation is picked up. Required: the default credential chain is not
	// wired, as for the writers.
	Credentials writer.CredentialProvider
	// Clock stamps the enqueue instant and the lease deadlines a receipt
	// carries, and judges them. Nil means clock.System.
	Clock clock.Clock
	// Policy is the delivery discipline. Its zero value is refused, as for
	// every broker; its VisibilityTimeout and its retry ceiling must also
	// fit MaxVisibility.
	Policy corequeue.PolicyValue
	// WaitTime is how long one Receive long-polls an empty queue before it
	// returns no message: 0 answers at once, at most MaxWaitTime.
	WaitTime time.Duration
}

// validate refuses, by field, a Config no broker can be built from. It sends
// nothing: every refusal here is decidable without SQS.
func (c *Config) validate() error {
	//: the policy first: its refusals are the domain's own.
	if perr := c.Policy.Validate(); perr != nil {
		//: QUEUE_MISCONFIGURED, naming the policy's field.
		return perr
	}
	//: where the requests go, then what SQS can carry.
	if targetErr := c.validateTarget(); targetErr != nil {
		//: named.
		return targetErr
	}
	//: the first bound that is wrong, in declaration order.
	return c.validateBounds()
}

// validateTarget refuses a missing queue, a dead-letter queue that is
// missing or the queue itself, a missing region and a missing signer.
func (c *Config) validateTarget() error {
	//: the first field that is wrong, in declaration order.
	switch {
	case c.QueueURL == "":
		//: named.
		return misconfigured("QueueURL")
	//: the broker owns the decision to dead-letter (ADR 0054): a last
	//: attempt with nowhere to go would lose the message.
	case c.DeadLetterQueueURL == "" || c.DeadLetterQueueURL == c.QueueURL:
		//: named.
		return misconfigured("DeadLetterQueueURL")
	case c.Region == "":
		//: named.
		return misconfigured("Region")
	case c.Credentials == nil:
		//: the default credential chain is not wired, as for the writers.
		return misconfigured("Credentials")
	default:
		//: every request has somewhere to go.
		return nil
	}
}

// validateBounds refuses a lease or a retry delay SQS cannot record, and a
// long poll outside what SQS waits.
func (c *Config) validateBounds() error {
	//: the first field that is wrong, in declaration order.
	switch {
	//: SQS records no lease, and no retry delay, longer than twelve hours.
	case c.Policy.VisibilityTimeout > MaxVisibility:
		//: named.
		return misconfigured("Policy.VisibilityTimeout")
	case max(c.Policy.RetryDelay, c.Policy.MaxRetryDelay) > MaxVisibility:
		//: named.
		return misconfigured("Policy.RetryDelay")
	//: long polling waits between nothing and twenty seconds.
	case c.WaitTime < 0 || c.WaitTime > MaxWaitTime:
		//: named.
		return misconfigured("WaitTime")
	default:
		//: a broker can be built.
		return nil
	}
}

// misconfigured refuses the configuration, naming the field at fault.
func misconfigured(field string) error {
	//: origin wins: the sentinel's identity, the field as a diagnostic.
	return errs.Wrap(SQSMisconfigured, errs.WrapParams{}, errs.String("field", field))
}

// clockOrSystem is the configured clock, the system's when none is.
func (c *Config) clockOrSystem() clock.Clock {
	//: nil means the wall clock.
	if c.Clock == nil {
		//: package time.
		return clock.System
	}
	//: the caller's, a test's ManualClock included.
	return c.Clock
}
