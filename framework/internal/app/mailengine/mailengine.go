package mailengine

import (
	"errors"
	"net"
	"strconv"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
	mailspool "github.com/kitsunium/sdk/pkg/v1/app/mail/spool"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// capturedMails is how many deliveries the capture transport keeps: one,
// which the mailer reads at once — the mailbox keeps what the Studio shows.
const capturedMails int = 1

// Enable plugs the mailers' outbox and their transports into the framework.
func Enable() {
	plug.Mail.Store(&plug.MailDriverConfig{
		Open: open, Validate: validate, Capture: capture,
		ParseSMTP: parseSMTP, NewSMTP: newSMTP, Refused: refused,
	})
}

// validate is the SDK's verdict on msg, a mail.Message: what the spool
// would refuse it for.
func validate(msg plug.MailValue) error {
	return mail.Validate(messageOf(msg))
}

// capture is the SDK's capture transport, keeping the last message it
// composed.
//
// IFACE-PLUGIN: kit reaches a transport through plug.MailSink; this is
// its implementation.
func capture() plug.MailSink {
	return &captured{t: mail.NewCapture(capturedMails)}
}

// parseSMTP reads url with the SDK's mail.ParseURL, which refuses what its
// SMTP transport would: the relay it names, its configuration — which holds
// the password — handed on to newSMTP and never kept.
func parseSMTP(url string) (plug.MailRelayValue, error) {
	cfg, err := mail.ParseURL(url)
	if err != nil {
		return plug.MailRelayValue{}, err
	}
	return plug.MailRelayValue{Config: cfg, Server: net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), TLS: tlsName(cfg.TLS)}, nil
}

// newSMTP is the SDK's SMTP transport to relay, as parseSMTP read it. It
// dials nothing.
//
// IFACE-PLUGIN: kit reaches a transport through plug.MailSink; this is
// its implementation.
func newSMTP(relay *plug.MailRelayValue) (plug.MailSink, error) {
	cfg, ok := relay.Config.(mail.SMTPConfig)
	if !ok {
		return nil, errs.Wrap(mail.InvalidConfig, errs.WrapParams{}, errs.String("problem", "the relay was not read by the mail subsystem"))
	}
	t, err := mail.NewSMTP(cfg)
	if err != nil {
		return nil, err
	}
	return transport{t: t}, nil
}

// tlsName is the TLS mode as the URL spells it: starttls, implicit or none.
func tlsName(mode mail.TLSMode) string {
	switch mode {
	case mail.TLSStartTLS:
		return "starttls"
	case mail.TLSImplicit:
		return "implicit"
	case mail.TLSDisabled:
		return "none"
	default:
		// A mode a parsed URL never holds: ParseURL refuses the unset one.
		return "unset"
	}
}

// sentinels are the SDK's sentinels of each refusal kit tells apart.
var sentinels = map[plug.MailRefusal]error{
	plug.MailRefusalClosed:      mailspool.Closed,
	plug.MailRefusalUnencodable: mailspool.MessageUnencodable,
	plug.MailRefusalInvalidURL:  mail.InvalidURL,
}

// refused reports whether err, or an error it wraps, is the SDK's sentinel
// of kind.
func refused(err error, kind plug.MailRefusal) bool {
	s, ok := sentinels[kind]
	return ok && errors.Is(err, s)
}

// messageOf is the SDK's mail v holds; the zero mail for any other value,
// which the SDK refuses.
func messageOf(v plug.MailValue) mail.Message {
	msg, _ := v.Value.(mail.Message)
	return msg
}

// addressOf is the SDK's sender v holds; none for any other value.
func addressOf(v plug.MailValue) mail.Address {
	from, _ := v.Value.(mail.Address)
	return from
}
