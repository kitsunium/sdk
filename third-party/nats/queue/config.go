package queue

import (
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// DefaultDuplicateWindow is the duplicate window of a stream
// Config.CreateStream creates — JetStream's own default — and so the one
// window PublishOnce accepts on such a stream.
const DefaultDuplicateWindow time.Duration = 2 * time.Minute

// Config configures NewJetStream: the connection, the stream, its subject and
// dead-letter subject, the durable consumer, and the delivery discipline.
type Config struct {
	// Conn is the caller's connection; the broker never closes it. Required.
	Conn *nats.Conn
	// Clock stamps the enqueue instant and the lease deadlines a receipt
	// carries, and judges them. Nil means clock.System.
	Clock clock.Clock
	// Stream names the JetStream stream that holds the queue. Required.
	Stream string
	// Subject is the subject messages are published on; the stream must
	// capture it. Required.
	Subject string
	// DeadLetterSubject is the subject a message is published on, with its
	// cause, once it is dead-lettered; the stream must capture it too, and
	// the consumer never reads it. Required.
	DeadLetterSubject string
	// Consumer is the durable pull consumer every broker of the queue shares. Required.
	Consumer string
	// Policy is the delivery discipline: VisibilityTimeout is AckWait. Its
	// zero value is refused, as for every broker.
	Policy corequeue.PolicyValue
	// Timeout bounds every request that waits on the server — a publication's
	// PubAck, an acknowledgement's confirmation. Zero is refused (ADR 0031).
	Timeout time.Duration
	// CreateStream creates the stream, over Subject and DeadLetterSubject,
	// and the consumer, when first use finds them missing. False answers
	// StreamMissing or ConsumerMissing instead.
	CreateStream bool
}

// validate refuses, by field, a Config no broker can be built from.
func (c *Config) validate() error {
	//: the policy first: its refusals are the domain's own.
	if perr := c.Policy.Validate(); perr != nil {
		//: QUEUE_MISCONFIGURED, naming the policy's field.
		return perr
	}
	//: the connection, then the names, in declaration order.
	if nameErr := c.validateNames(); nameErr != nil {
		//: named.
		return nameErr
	}
	//: ADR 0031: "no bound" and "fail at once" are opposite readings.
	if c.Timeout <= 0 {
		//: named.
		return misconfigured("Timeout")
	}
	//: a broker can be built.
	return nil
}

// validateNames refuses a missing connection, and a stream, subject,
// dead-letter subject or consumer JetStream would not accept.
func (c *Config) validateNames() error {
	//: the first field that is wrong, in declaration order.
	switch {
	case c.Conn == nil:
		//: named.
		return misconfigured("Conn")
	case !validName(c.Stream):
		//: named.
		return misconfigured("Stream")
	case !validSubject(c.Subject):
		//: named.
		return misconfigured("Subject")
	//: the dead letters must never be read back as work.
	case !validSubject(c.DeadLetterSubject) || c.DeadLetterSubject == c.Subject:
		//: named.
		return misconfigured("DeadLetterSubject")
	case !validName(c.Consumer):
		//: named.
		return misconfigured("Consumer")
	default:
		//: every name is one JetStream accepts.
		return nil
	}
}

// validName reports whether name is a stream or consumer name JetStream
// accepts: not empty, no whitespace, no dot, no wildcard, no separator.
func validName(name string) bool {
	//: the characters a JetStream name may not hold.
	return name != "" && !strings.ContainsAny(name, " \t\r\n.*>/\\")
}

// validSubject reports whether subject is a literal subject: tokens
// separated by dots, none empty, no wildcard.
func validSubject(subject string) bool {
	//: empty, whitespace or a wildcard is no literal subject.
	if subject == "" || strings.ContainsAny(subject, " \t\r\n*>") {
		return false
	}
	//: no empty token.
	return !strings.Contains("."+subject+".", "..")
}

// misconfigured refuses the configuration, naming the field at fault.
func misconfigured(field string) error {
	//: origin wins: the sentinel's identity, the field as a diagnostic.
	return errs.Wrap(JetStreamMisconfigured, errs.WrapParams{}, errs.String("field", field))
}

// clockOrSystem is the configured clock, the system's when none is.
func (c *Config) clockOrSystem() clock.Clock {
	//: nil means the wall clock.
	if c.Clock == nil {
		//: package time.
		return clock.System
	}
	//: the caller's.
	return c.Clock
}
