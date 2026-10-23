package forward

import (
	"context"
	"time"

	coreforward "github.com/kitsunium/sdk/internal/core/app/forward"
	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// The defaults a zero field clamps to (ADR 0031: each zero has one reading —
// "not thought about" — and the value below is the one that needs no
// explanation).
const (
	// DefaultMaxBatches bounds the batches a forwarder holds when
	// Config.MaxBatches is zero — the order of magnitude of the OpenTelemetry
	// Collector's sending_queue.
	DefaultMaxBatches int = 10_000
	// DefaultMaxBytes bounds the bytes a forwarder holds when Config.MaxBytes
	// is zero — Vector's smallest disk buffer.
	DefaultMaxBytes int64 = 256 << 20
	// DefaultBlockTimeout is how long Enqueue waits for room under Block when
	// Config.BlockTimeout is zero.
	DefaultBlockTimeout time.Duration = 5 * time.Second
	// DefaultSendTimeout bounds one attempt when Config.SendTimeout is zero —
	// the OTLP specification's default OTEL_EXPORTER_OTLP_TIMEOUT.
	DefaultSendTimeout time.Duration = 10 * time.Second
	// DefaultMaxAge is how long after the queue accepted it a batch may still
	// be sent when Config.MaxAge is zero.
	DefaultMaxAge time.Duration = 24 * time.Hour
	// DefaultRetryBase and DefaultRetryMax bound the wait between attempts
	// when Config.Backoff is zero: five seconds, growing to thirty — the
	// Collector's retry_on_failure numbers.
	DefaultRetryBase time.Duration = 5 * time.Second
	// DefaultRetryMax is the ceiling of the default backoff.
	DefaultRetryMax time.Duration = 30 * time.Second
	// DefaultRecountInterval is how long the counts may go without the queue
	// being counted again when Config.RecountInterval is zero: a backlog
	// another process changed is said within a minute of a read.
	DefaultRecountInterval time.Duration = time.Minute
)

// countPage is how many live messages a count of the queue reads per call.
const countPage int = 256

// The three full policies. The zero is DropOldest.
const (
	// DropOldest takes the oldest batches the consumer is not sending out of
	// the queue, each counted Dropped, until the new one fits; when none can
	// be taken it refuses the new one as DropNewest does.
	DropOldest FullPolicy = iota
	// DropNewest refuses the new batch with ForwardFull, counted Dropped.
	DropNewest
	// Block waits for room up to Config.BlockTimeout, then refuses the new
	// batch with ForwardFull, counted Dropped.
	Block
)

// The names String renders.
const (
	dropOldestName string = "drop-oldest"
	dropNewestName string = "drop-newest"
	blockName      string = "block"
	unknownName    string = "unknown"
)

// Sender ships one batch, as the forwarder stored it, and returns the
// verdict the forwarder classifies with Config.Retryable. It is called on the
// forwarder's consumer, under a context bounded by Config.SendTimeout, and
// may be called again with the same bytes — after a retryable failure, or
// after a crash between its success and the acknowledgement.
//
// An OTLP/HTTP exporter's ExportDocument has its shape (trace and metrics
// DocumentExporter).
type Sender func(ctx context.Context, batch []byte) error

// FullPolicy says what Enqueue does with a batch the queue has no room for.
type FullPolicy uint8

// Config configures [New]. Broker, Send and Retryable are required; every
// other field is optional.
type Config struct {
	// Broker is the queue the forwarder holds its batches in: a directory
	// (queue.NewFile) for a backlog that outlives the process, a table
	// (queue.NewSQL), or memory. One queue per signal. Build it with a
	// VisibilityTimeout above SendTimeout, a MaxDeliveries no outage reaches
	// — MaxAge is the forwarder's budget — and a MaxMessageBytes that holds
	// a batch. The forwarder neither sets its policy nor closes it.
	Broker corequeue.Broker
	// Send ships one stored batch: an OTLP/HTTP exporter's ExportDocument,
	// or any function of that shape.
	Send Sender
	// Retryable classifies a failure of Send as one a later attempt may cure
	// — trace.OTLPRetryable, metrics.OTLPRetryable. It is REQUIRED: nil reads
	// as "retry everything" or as "retry nothing", two opposites. A failure
	// it calls permanent is dead-lettered at once.
	Retryable func(error) bool
	// Clock paces every retry, bounds every attempt and the wait of Block,
	// and ages every batch. Nil means clock.System; a clock.ManualClock drives
	// a test without a sleep. It must be the broker's: a batch's age is read
	// from the instant the broker stamped.
	Clock clock.Timed
	// MaxBatches bounds the batches the forwarder holds. Zero means
	// DefaultMaxBatches; negative is refused.
	MaxBatches int
	// MaxBytes bounds the bytes the forwarder holds. Zero means
	// DefaultMaxBytes; negative is refused. A batch larger on its own is
	// refused with BatchTooLarge.
	MaxBytes int64
	// WhenFull is what Enqueue does when either bound is reached. The zero
	// is DropOldest.
	WhenFull FullPolicy
	// BlockTimeout bounds the wait of Block. Zero means DefaultBlockTimeout;
	// negative is refused.
	BlockTimeout time.Duration
	// SendTimeout bounds one attempt, on Clock. Zero means
	// DefaultSendTimeout; negative is refused.
	SendTimeout time.Duration
	// MaxAge is how long after the queue accepted it a batch may still be
	// sent: one delivered older is dropped unsent, one whose retryable
	// failure finds it older is not retried; either is acknowledged and
	// counted Expired. A Retry-After is honoured in full, but never past it.
	// Zero means DefaultMaxAge; negative is refused.
	MaxAge time.Duration
	// Backoff is the wait before each retry when the failure imposes none:
	// Backoff.Delay(attempt). The zero value is the
	// DefaultRetryBase–DefaultRetryMax curve, and a curve without a positive
	// BaseDelay starts from DefaultRetryBase under its own ceiling: a failed
	// batch is never retried at once.
	Backoff kbackoff.Value
	// Parallelism is how many consumers send at once. Non-positive is one,
	// which keeps the queue's order while nothing fails and sends one request
	// at a time to a collector that is down; above one there is no order.
	Parallelism int
	// PollInterval is how late a batch another PROCESS queued may be
	// noticed; one Enqueue queued in this process is sent at once. Zero is
	// the queue's own default.
	PollInterval time.Duration
	// DrainTimeout is how long the batch being sent when Run's context ends
	// may still run. Zero stops it at once; it is then handed back uncounted
	// and sent by the next process.
	DrainTimeout time.Duration
	// SettleTimeout bounds each acknowledgement and hand-back once Run's
	// context has ended. Zero is the queue's own default.
	SettleTimeout time.Duration
	// RecountInterval is how long the counts go before the queue is counted
	// again, over a broker that lists its messages: a read of Stats, or a
	// batch leaving the queue, once it has passed has Run list the queue and
	// count what another process changed in it — a dead letter replayed into
	// it, a batch another consumer took. The delivery of a batch the
	// forwarder did not count has it counted at once, once per interval at
	// most. A count reads every live batch, its payload included, on Run's
	// goroutine and never on
	// Enqueue's: it costs what the backlog weighs. Zero means
	// DefaultRecountInterval; negative is refused.
	RecountInterval time.Duration
}

// String renders the policy for a log line; a value this package never mints
// renders as "unknown".
func (p FullPolicy) String() string {
	//: one name per policy.
	switch p {
	//: the zero.
	case DropOldest:
		//: its name.
		return dropOldestName
	//: refuse the newest.
	case DropNewest:
		//: its name.
		return dropNewestName
	//: wait for room.
	case Block:
		//: its name.
		return blockName
	//: anything else.
	default:
		//: a value never minted here.
		return unknownName
	}
}

// validate refuses a configuration no forwarder can run under.
func (c *Config) validate() error {
	//: the three required fields.
	switch {
	//: nowhere to hold a batch.
	case c.Broker == nil:
		//: named by field.
		return misconfigured("Broker", "missing")
	//: nothing to send with.
	case c.Send == nil:
		//: named by field.
		return misconfigured("Send", "missing")
	//: no verdict on a failure.
	case c.Retryable == nil:
		//: named by field.
		return misconfigured("Retryable", "missing")
	//: an unknown policy.
	case c.WhenFull > Block:
		//: named by field.
		return misconfigured("WhenFull", "unknown")
	//: required fields present.
	default:
		//: the bounds next.
		return c.validateBounds()
	}
}

// validateBounds refuses a negative bound or timeout: a zero is a default,
// a negative has no reading.
func (c *Config) validateBounds() error {
	//: each signed knob.
	switch {
	//: negative batch bound.
	case c.MaxBatches < 0:
		//: named by field.
		return misconfigured("MaxBatches", "negative")
	//: negative byte bound.
	case c.MaxBytes < 0:
		//: named by field.
		return misconfigured("MaxBytes", "negative")
	//: negative wait.
	case c.BlockTimeout < 0:
		//: named by field.
		return misconfigured("BlockTimeout", "negative")
	//: negative attempt bound.
	case c.SendTimeout < 0:
		//: named by field.
		return misconfigured("SendTimeout", "negative")
	//: negative age.
	case c.MaxAge < 0:
		//: named by field.
		return misconfigured("MaxAge", "negative")
	//: negative interval between counts.
	case c.RecountInterval < 0:
		//: named by field.
		return misconfigured("RecountInterval", "negative")
	//: every knob readable.
	default:
		//: accepted.
		return nil
	}
}

// normalized resolves every clamped field, so the engine reads numbers rather
// than remembering defaults.
func (c *Config) normalized() Config {
	n := *c
	//: the clock.
	if n.Clock == nil {
		//: the wall clock.
		n.Clock = clock.System
	}
	n.MaxBatches = orDefault(n.MaxBatches, DefaultMaxBatches)
	n.MaxBytes = orDefault(n.MaxBytes, DefaultMaxBytes)
	n.BlockTimeout = orDefault(n.BlockTimeout, DefaultBlockTimeout)
	n.SendTimeout = orDefault(n.SendTimeout, DefaultSendTimeout)
	n.MaxAge = orDefault(n.MaxAge, DefaultMaxAge)
	n.RecountInterval = orDefault(n.RecountInterval, DefaultRecountInterval)
	n.Parallelism = max(n.Parallelism, 1)
	//: a curve that starts at nothing would retry at once.
	if n.Backoff.BaseDelay <= 0 {
		//: the default start.
		n.Backoff.BaseDelay = DefaultRetryBase
		//: and the default ceiling when none was named.
		if n.Backoff.MaxDelay <= 0 {
			//: thirty seconds.
			n.Backoff.MaxDelay = DefaultRetryMax
		}
	}
	//: resolved.
	return n
}

// orDefault returns value, or fallback when value is zero.
func orDefault[T int | int64 | time.Duration](value, fallback T) T {
	//: zero is the one reading "not thought about".
	if value == 0 {
		//: the default.
		return fallback
	}
	//: the caller's.
	return value
}

// misconfigured is ForwardMisconfigured naming the field and the problem.
func misconfigured(field, problem string) error {
	//: the sentinel, with where and why.
	return kerrs.Wrap(coreforward.ForwardMisconfigured, kerrs.WrapParams{},
		kerrs.String("field", field), kerrs.String("problem", problem))
}
