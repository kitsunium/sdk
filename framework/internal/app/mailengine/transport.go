package mailengine

import (
	"context"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
)

// The transports a mailer hands its mail to, as kit's core holds them
// (plug.MailSink), and the transport the spool hands each attempt to:
// kit's, around them.

// transport is the SDK's SMTP transport as kit's core uses it.
type transport struct {
	t mail.Transport
}

// captured is the SDK's capture transport as kit's core uses it: it
// delivers nothing and keeps the last message it composed.
type captured struct {
	t mail.FullTransport
}

// attempts is the transport the spool hands each attempt to: kit's core's
// attempt, told the attempt the spool's context carries.
type attempts struct {
	run func(ctx context.Context, msg plug.MailValue, attempt plug.MailAttemptValue) error
}

// Send delivers msg, a mail.Message.
func (t transport) Send(ctx context.Context, msg plug.MailValue) error {
	return t.t.Send(ctx, messageOf(msg))
}

// Send composes msg, a mail.Message, and keeps it.
func (c *captured) Send(ctx context.Context, msg plug.MailValue) error {
	return c.t.Send(ctx, messageOf(msg))
}

// Composed is the last message the transport composed, nil for none.
func (c *captured) Composed() []byte {
	sent := c.t.Sent()
	if len(sent) == 0 {
		return nil
	}
	return sent[len(sent)-1].Raw
}

// Send runs kit's attempt of msg, with the attempt the spool put in ctx.
func (a attempts) Send(ctx context.Context, msg mail.Message) error {
	attempt, _ := attemptFrom(ctx)
	return a.run(ctx, plug.MailValue{Value: msg}, attempt)
}
