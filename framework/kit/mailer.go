package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Mailer is outbound mail: a durable outbox and the transport that empties
// it. [Mailer].Send only queues — it returns once the message is safely in
// the outbox — and the mailer's own loop hands each message to the transport,
// retrying a failure and dead-lettering a message after its last attempt.
//
// The transport is the SDK's mail package: SMTP when the mail connector's
// secret smtp-url is set — KIT_SMTP_URL, smtp://user:password@host:587?tls=
// starttls|implicit|none — and otherwise the capture transport, which keeps
// every message for the Studio's mailbox and for tests ([Mailer].Captured)
// and delivers nothing. Outside dev, the capture transport is reported as a
// warning. The URL is read again before every delivery: replacing it where
// it lives — a new version in the store, a new file behind
// KIT_SMTP_URL_FILE — takes effect at the next mail.
type Mailer = ikit.Mailer

// MailerOption configures a mailer.
type MailerOption = ikit.MailerConfigurer

// from is From's body: decl_gen.go writes From, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func from(name, addr string) MailerOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.From(name, addr)
}

// mailAttempts is MailAttempts's body: decl_gen.go writes MailAttempts, from the
// design, as one call of it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func mailAttempts(n int) MailerOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.MailAttempts(n)
}
