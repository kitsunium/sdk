package plug

import "context"

// The mail subsystem's spool and transport (ADR 0179 §3, step 6), which
// framework/internal/kit/mailkit implements over the SDK's mail spool and
// its transports.

// MailSpool is one mailer's outbox as kit's core uses it: a mail put in it
// at once or under an ID kit minted, the loop that hands each mail to the
// transport, and its dead letters.
type MailSpool interface {
	// Send puts msg in the outbox — in the transaction ctx carries for its
	// database, when it lives on one — and returns its outbox ID.
	Send(ctx context.Context, msg MailValue) (string, error)
	// SendWithID puts msg in the outbox under id, minted by kit.
	SendWithID(ctx context.Context, id string, msg MailValue) error
	// DeadLetters counts up to maxLetters of the mails earlier runs gave up
	// on.
	DeadLetters(ctx context.Context, maxLetters int) (int, error)
	// Run hands each mail to the transport until ctx ends.
	Run(ctx context.Context) error
	// Close releases the outbox.
	Close() error
}

// MailSink is what a mailer hands a mail to: the SDK's SMTP transport,
// or its capture transport, which delivers nothing. It grows by the sibling
// below.
type MailSink interface {
	// Send delivers msg.
	Send(ctx context.Context, msg MailValue) error
}

// MailCaptureSource is a transport that keeps what it composed: the capture
// transport, whose last message the Studio's mailbox shows. A sibling of
// [MailSink].
type MailCaptureSource interface {
	// Composed is the last message the transport composed, nil for none.
	Composed() []byte
}
