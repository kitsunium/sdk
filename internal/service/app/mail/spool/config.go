package spool

import (
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/app/mail"
	corespool "github.com/kitsunium/sdk/internal/core/app/mail/spool"
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
		if addrErr := svcmail.ValidateAddress(coremail.HeaderFrom, c.From); addrErr != nil {
			//: SpoolMisconfigured, with the address verdict's reason.
			return kerrs.Wrap(corespool.SpoolMisconfigured, kerrs.WrapParams{},
				kerrs.String("setting", "From"), kerrs.String("problem", kerrs.PublicOf(addrErr)))
		}
	}
	//: a spool that can run.
	return nil
}

// misconfigured is SpoolMisconfigured naming a setting and its problem.
func misconfigured(setting, problem string) error {
	//: the two fields every configuration refusal carries.
	return kerrs.Wrap(corespool.SpoolMisconfigured, kerrs.WrapParams{},
		kerrs.String("setting", setting), kerrs.String("problem", problem))
}
