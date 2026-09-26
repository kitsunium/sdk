// Package spool — the spool's construction parameters, the defaults their
// zero values clamp to, and the refusals.
package spool

import (
	"context"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// The defaults a zero field clamps to (ADR 0031: each zero has one reading —
// "not thought about" — and the value below is the one that needs no
// explanation).
const (
	// DefaultSendTimeout bounds one hand-over to the transport. The lease a
	// delivery holds is twice it, so a slow relay can never make the queue
	// hand the same mail to a second attempt while the first is still talking.
	DefaultSendTimeout time.Duration = time.Minute
	// DefaultRetryBase and DefaultRetryMax bound the wait between attempts:
	// one second after the first failure, doubling, never more than five
	// minutes. A zero backoff would retry at once — a relay that is down would
	// be dialled as fast as the queue can lease.
	DefaultRetryBase time.Duration = time.Second
	DefaultRetryMax  time.Duration = 5 * time.Minute
	// DefaultMaxMessageBytes bounds one spooled mail, attachments included,
	// as the record the spool writes. It is above the queue's own default
	// because a mail with an attachment is routinely a few megabytes.
	DefaultMaxMessageBytes int = 32 << 20
	// DeliveredMemory is how many delivered mails the spool remembers, by ID,
	// to drop a redelivery rather than send it twice.
	DeliveredMemory int = 1024
)

// Config configures [New]. Transport and MaxAttempts are required; every
// other field is optional.
type Config struct {
	// Transport delivers each mail: the SMTP transport, the capture transport
	// in development and tests, a provider's connector.
	Transport coremail.Transport
	// Clock stamps every mail and every event, bounds every attempt and paces
	// every retry. Nil means clock.System; a clock.ManualClock drives a test's
	// retries without a sleep.
	Clock clock.Timed
	// Observe is told what happens to every mail — queued, sent, retrying,
	// dead-lettered, dropped as a duplicate — one call at a time, on the
	// caller of Send or the spool's own consumer. It must be short. Nil tells
	// nobody: the spool writes nothing anywhere itself.
	Observe func(EventValue)
	// Annotate returns what Send's context knows that a delivery should know
	// too — a trace to continue, the caller's identity — kept with the mail
	// and handed back to every attempt through AttemptFrom. Nil keeps nothing.
	// It must not return a secret: it is written into the spool as it is.
	Annotate func(ctx context.Context) map[string]string
	// NewID mints the spool's identifier for a mail. Nil mints a ULID.
	NewID func() (string, error)
	// Dir is the spool's directory: a durable queue that outlives the
	// process, shared by every process that names it. Empty keeps the spool
	// in memory, and a process that ends loses what it had not delivered.
	Dir string
	// From is the sender of every mail that names none. It must be a usable
	// address when set.
	From coremail.AddressValue
	// Backoff is the wait before each retry: Backoff.Delay(attempt). The zero
	// value is the DefaultRetryBase–DefaultRetryMax curve.
	Backoff svcres.BackoffValue
	// MaxAttempts is how many deliveries a mail gets before it is
	// dead-lettered with its last failure. It is REQUIRED: zero reads as
	// "unlimited" or as "none", two opposites, and neither is a mail spool.
	MaxAttempts int
	// SendTimeout bounds one attempt. Not positive means DefaultSendTimeout.
	SendTimeout time.Duration
	// MaxMessageBytes bounds one spooled mail. Zero means
	// DefaultMaxMessageBytes; negative is refused.
	MaxMessageBytes int
	// PollInterval is how late a mail another PROCESS spooled into Dir may be
	// noticed; a mail spooled by this process is delivered at once. Zero is
	// the queue's own default.
	PollInterval time.Duration
}

// validate refuses a spool that could never deliver.
func (c *Config) validate() error {
	//: nothing to hand a mail to.
	if c.Transport == nil {
		//: SpoolMisconfigured, naming the setting.
		return misconfigured("Transport", "nil")
	}
	//: the attempt budget, whose two zero readings are opposites.
	if c.MaxAttempts <= 0 {
		//: SpoolMisconfigured, naming the setting.
		return misconfigured("MaxAttempts", "not positive")
	}
	//: a negative bound is not a bound.
	if c.MaxMessageBytes < 0 {
		//: SpoolMisconfigured, naming the setting.
		return misconfigured("MaxMessageBytes", "negative")
	}
	//: a default sender every mail would be refused for.
	if !c.From.IsZero() {
		//: the mail domain's own verdict on the address.
		if addrErr := coremail.ValidateAddress(coremail.HeaderFrom, c.From); addrErr != nil {
			//: SpoolMisconfigured, with the address verdict's reason.
			return kerrs.Wrap(SpoolMisconfigured, kerrs.WrapParams{},
				kerrs.String("setting", "From"), kerrs.String("problem", kerrs.PublicOf(addrErr)))
		}
	}
	//: a spool that can run.
	return nil
}

// misconfigured is SpoolMisconfigured naming a setting and its problem.
func misconfigured(setting, problem string) error {
	//: the two fields every configuration refusal carries.
	return kerrs.Wrap(SpoolMisconfigured, kerrs.WrapParams{},
		kerrs.String("setting", setting), kerrs.String("problem", problem))
}
