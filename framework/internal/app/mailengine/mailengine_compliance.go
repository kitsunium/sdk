package mailengine

import (
	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// : Asserts at compile time that a mailer's outbox and its transports are
// what plug describes, that the transport the spool hands each attempt to is
// the SDK's, and that the app's clock is the SDK's.
var (
	_ plug.MailSpool         = spool{}
	_ plug.MailSink          = transport{}
	_ plug.MailSink          = (*captured)(nil)
	_ plug.MailCaptureSource = (*captured)(nil)

	_ mail.Transport = attempts{}

	_ clock.Timed = sdkClock{}
)
