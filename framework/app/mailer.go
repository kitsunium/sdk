package app

import (
	"context"
	"time"

	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Mailer is outbound mail: a durable outbox and the transport that empties
// it. [Mailer].Send only queues — it returns once the message is safely in
// the outbox — and the mailer's own loop hands each message to the transport,
// retrying a failure and dead-lettering a message after its last attempt.
//
// The transport is the SDK's mail package: SMTP when the mail connector's
// secret smtp-url is set — <PRODUCT>_SMTP_URL, smtp://user:password@host:587?tls=
// starttls|implicit|none — and otherwise the capture transport, which keeps
// every message for the Studio's mailbox and for tests ([Mailer].Captured)
// and delivers nothing. Outside dev, the capture transport is reported as a
// warning. The URL is read again before every delivery: replacing it where
// it lives — a new version in the store, a new file behind
// <PRODUCT>_SMTP_URL_FILE — takes effect at the next mail. The outbox and the
// transports are the SDK's, which the mail subsystem plugs in
// (framework/app/mail, ADR 0179 step 6).
type Mailer = iapp.Mailer

// MailerOption configures a mailer.
type MailerOption = iapp.MailerConfigurer

// From sets the sender of every message that names none.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func From(name, addr string) MailerOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.From(name, addr)
}

// MailAttempts is how many deliveries a message gets before it is
// dead-lettered. The default is 6, backing off from one second.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func MailAttempts(n int) MailerOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.MailAttempts(n)
}

// Bounce is a mail its mailer gave up on: the receiving server refused it for
// good, it outlived its mailer's max-age, or its last attempt failed —
// GivenUp says which. It stays in the outbox as a dead letter.
//
// Reply is the receiving server's own answer: the hook may read it, log it or
// act on it, but the framework never puts it in a message it shows — the
// Studio's mailbox keeps its code alone.
type Bounce = iapp.BounceEvent

// OnBounce replaces what the mailer does with a mail it gives up — by
// default, it logs it as an error naming why — with fn: mark an address
// undeliverable, tell an operator, send it another way. fn runs on a
// goroutine of its own once the mailbox is updated — the outbox goes on
// meanwhile, and fn may send mail —, with a context on the mailer's node that
// continues the trace of the Send; the mailer's stop waits for it. A panic in
// fn is logged and changes nothing else. A nil fn keeps the framework's own.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OnBounce(fn func(ctx context.Context, b *Bounce)) MailerOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.OnBounce(fn)
}

// MailMaxAge is how long after it was queued a mail may still be retried: an
// attempt that fails later gives it up, as expired, whatever attempts it had
// left. It is the default of the mailer's setting <mailer>-max-age, which
// config/<env>.* and <PRODUCT>_<MAILER>_MAX_AGE override; 0 sets no age. The
// default is 5 days (RFC 5321 §4.5.4.1).
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func MailMaxAge(d time.Duration) MailerOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.MailMaxAge(d)
}
