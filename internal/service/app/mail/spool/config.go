package spool

import (
	"context"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/app/mail"
	corespool "github.com/kitsunium/sdk/internal/core/app/mail/spool"
	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcmail "github.com/kitsunium/sdk/internal/service/app/mail"
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
	// caller of Send or the spool's own consumer. A mail's Queued event comes
	// before every event of its delivery, even when the consumer delivers it
	// before Send returns. It must be short, and must not call Send, which
	// would wait for the call it is made from. Nil tells nobody: the spool
	// writes nothing anywhere itself.
	Observe func(EventValue)
	// Annotate returns what Send's context knows that a delivery should know
	// too — a trace to continue, the caller's identity — kept with the mail
	// and handed back to every attempt through AttemptFrom. Nil keeps nothing.
	// It must not return a secret: it is written into the spool as it is.
	Annotate func(ctx context.Context) map[string]string
	// NewID mints the spool's identifier for a mail Send queues; SendWithID
	// takes its caller's instead. Nil mints a ULID. Every identifier must be
	// new: the spool drops a mail whose identifier it delivered already, as a
	// redelivery. One that is empty, longer than MaxIDBytes or not an RFC 5322
	// dot-atom is refused at Send (SpoolMisconfigured), since it would become
	// the left half of the mail's Message-ID.
	NewID func() (string, error)
	// Dir is the spool's directory: a durable queue that outlives the
	// process, shared by every process that names it. Empty keeps the spool
	// in memory, and a process that ends loses what it had not delivered.
	// It must stay empty beside a Broker.
	Dir string
	// Broker is the queue the spool writes into and delivers from, built by
	// its caller instead of from Dir — a queue on SQL, so that a mail Send
	// queues inside the caller's transaction exists if and only if that
	// transaction commits (ADR 0151, ADR 0178). Nil builds the spool's own,
	// from Dir. The spool neither sets its policy nor closes it: build it
	// with MaxDeliveries equal to MaxAttempts, a VisibilityTimeout of at
	// least twice SendTimeout and a MaxMessageBytes that holds a mail with
	// its attachments, as the spool's own queue has, and close it after
	// Close. Dir and MaxMessageBytes describe the queue the spool would
	// build, and are refused beside it (SpoolMisconfigured).
	Broker corequeue.Broker
	// Deferrer is the transaction manager of Broker's database when Broker
	// joins its caller's transaction — the Transactor queue.NewSQL was given
	// — and must also be a core/data/sql Aborter, which the SDK's
	// transactor is. One field for both, as queue.NewSQL takes one
	// Transactor for its Joiner and Deferrer: the two halves of one
	// outcome cannot come from two managers.
	//
	// With it, a Send made inside one of its transactions tells Observe
	// EventQueued after the commit, and nothing at all when the work is
	// undone — the transaction or the savepoint the Send ran in rolled
	// back —, and a delivery of the mail still waits for that EventQueued.
	// A Send outside a transaction tells it at once. While such a
	// transaction is open, a delivery of a mail under the same identifier
	// waits for it to end.
	//
	// Nil tells EventQueued when Send returns, which suits a Broker that
	// keeps no transaction; beside a Broker that joins one, it would
	// announce a mail a rollback then takes back. Set without a Broker, or
	// to a manager that is not an Aborter, it is refused
	// (SpoolMisconfigured).
	Deferrer coresql.Deferrer
	// From is the sender of every mail that names none. It must be a usable
	// address when set.
	From coremail.AddressValue
	// Backoff is the wait before each retry: Backoff.Delay(attempt). The zero
	// value is the DefaultRetryBase–DefaultRetryMax curve, and a curve without
	// a positive BaseDelay starts from DefaultRetryBase under its own ceiling:
	// a failed mail is never retried at once.
	Backoff kbackoff.Value
	// MaxAttempts is how many deliveries a mail gets before it is
	// dead-lettered with its last failure. It is REQUIRED: zero reads as
	// "unlimited" or as "none", two opposites, and neither is a mail spool.
	MaxAttempts int
	// SendTimeout bounds one attempt. Not positive means DefaultSendTimeout.
	SendTimeout time.Duration
	// MaxMessageBytes bounds one spooled mail in the queue the spool builds.
	// Zero means DefaultMaxMessageBytes; negative is refused, and so is any
	// other value beside a Broker, whose own policy bounds a mail.
	MaxMessageBytes int
	// PollInterval is how late a mail another PROCESS spooled into Dir may be
	// noticed; a mail spooled by this process is delivered at once. Zero is
	// the queue's own default.
	PollInterval time.Duration
	// MaxAge is how long after QueuedAt a mail may still be retried: an
	// attempt that fails once MaxAge has passed on Clock dead-letters the
	// mail with that failure, marked MailExpired, whatever attempts it had
	// left — RFC 5321 §4.5.4.1 gives up after four to five days. The age is
	// read when an attempt fails, so a mail is attempted at least once
	// however late it is delivered. Zero sets no age: MaxAttempts alone
	// decides. Negative is refused (SpoolMisconfigured).
	MaxAge time.Duration
	// RetryPermanent retries a permanent refusal like a temporary one. By
	// default a failure carrying an SMTP 5xx reply — a *net/textproto.Error
	// with a Code from 500 to 599 anywhere in its chain — dead-letters the
	// mail at once, marked MailRejected, because RFC 5321 §4.2.1 forbids
	// repeating a 5yz command unchanged; a 4xx reply, a 552 to RCPT — read
	// as 452, too many recipients, by RFC 5321 §4.5.3.1.10 — and a failure
	// without a reply are retried. Set it for a relay known to answer 5xx
	// for a condition that passes.
	RetryPermanent bool
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
	//: a negative age is no age, and zero already says "none".
	if c.MaxAge < 0 {
		//: SpoolMisconfigured, naming the setting.
		return misconfigured("MaxAge", "negative")
	}
	//: a caller's queue, and the transaction manager that announces after it.
	if brokerErr := c.validateBroker(); brokerErr != nil {
		//: SpoolMisconfigured, naming the setting.
		return brokerErr
	}
	//: a default sender every mail would be refused for.
	if !c.From.IsZero() {
		//: the mail domain's own verdict on the address.
		if addrErr := svcmail.ValidateAddress(coremail.HeaderFrom, c.From); addrErr != nil {
			//: SpoolMisconfigured, with the address verdict's reason.
			return kerrs.Wrap(corespool.SpoolMisconfigured, kerrs.WrapParams{},
				kerrs.String("setting", "From"), kerrs.String("problem", kerrs.PublicOf(addrErr)))
		}
	}
	//: a spool that can run.
	return nil
}

// validateBroker refuses the settings a caller's Broker makes inert, and a
// Deferrer that cannot announce what a Broker joined or take it back.
func (c *Config) validateBroker() error {
	//: the settings of the queue the spool builds, beside one it does not.
	if c.Broker != nil {
		//: a directory the spool would never open.
		if c.Dir != "" {
			//: SpoolMisconfigured, naming the setting.
			return misconfigured("Dir", "set beside Broker")
		}
		//: a bound the caller's broker would never apply.
		if c.MaxMessageBytes != 0 {
			//: SpoolMisconfigured, naming the setting.
			return misconfigured("MaxMessageBytes", "set beside Broker, whose own policy bounds a mail")
		}
	}
	//: no transaction manager: Send announces when it returns.
	if c.Deferrer == nil {
		//: nothing more to check.
		return nil
	}
	//: a queue the spool builds never joins a transaction, and a mail in it
	//: would be announced after a commit that never decided it.
	if c.Broker == nil {
		//: SpoolMisconfigured, naming the setting.
		return misconfigured("Deferrer", "set without Broker")
	}
	//: without the abort half, a rollback would leave a mail's registration
	//: open, and every later delivery of its identifier waiting for it.
	if _, aborts := c.Deferrer.(coresql.Aborter); !aborts {
		//: SpoolMisconfigured, naming the setting.
		return misconfigured("Deferrer", "not a core/data/sql Aborter")
	}
	//: a transaction manager the spool can announce through.
	return nil
}

// misconfigured is SpoolMisconfigured naming a setting and its problem.
func misconfigured(setting, problem string) error {
	//: the two fields every configuration refusal carries.
	return kerrs.Wrap(corespool.SpoolMisconfigured, kerrs.WrapParams{},
		kerrs.String("setting", setting), kerrs.String("problem", problem))
}
