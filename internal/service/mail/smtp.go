// Package mail — the SMTP transport.
//
// It is built ON net/smtp and does not reimplement RFC 5321. The package is
// FROZEN upstream — its own documentation says "The smtp package is frozen and
// is not accepting new features" — which is a statement about features and not
// about maintenance: it still receives security fixes, and the protocol it
// implements has not gained a feature this domain wants since RFC 3207.
//
// What this package therefore commits to maintaining is the POLICY above it,
// which is where every decision that matters lives: that a required STARTTLS
// is refused rather than downgraded, that credentials never travel over an
// unencrypted session, that the verified TLS name is set (net/smtp does not set
// it), and that a context actually stops a send (net/smtp has no context at
// all). If net/smtp is ever deprecated rather than frozen, the client is
// replaced here and [coremail.Transport] does not change — which is the reason
// the port has one method and no session on it.
package mail

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/smtp"
	"strconv"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// SMTP extension names, as they appear in an EHLO response.
const (
	extensionStartTLS string = "STARTTLS"
	extensionAuth     string = "AUTH"
)

// smtpTransport dials a server per Send. It holds no connection between calls.
type smtpTransport struct {
	cfg      SMTPConfig
	composer *Composer
	address  string
}

// NewSMTP returns a transport that delivers over SMTP, refusing at
// CONSTRUCTION any configuration it could not honour.
//
// Construction-time refusal is the point. A transport whose TLS mode is unset,
// or that carries a password it may not send, is a misconfiguration whose
// symptom would otherwise be a failed send at three in the morning — while the
// fix is one line in the wiring, where this error is raised.
//
// It deliberately holds NO pooled connection. An SMTP session is stateful, a
// pooled one must be revalidated with a NOOP before every reuse (a server may
// have closed it minutes ago and the socket will not say so until a write
// fails), and the caller that wants one session for many messages already has
// a supported way to ask: [coremail.BatchSender].
func NewSMTP(cfg SMTPConfig) (transport coremail.Transport, err error) {
	//: everything refusable is refused here, not at send time.
	if cfgErr := cfg.validate(); cfgErr != nil {
		//: InvalidConfig or AuthInsecure.
		return nil, cfgErr
	}
	//: the address is fixed for the transport's lifetime.
	address := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	//: the same composer the memory transport uses.
	return &smtpTransport{cfg: cfg, composer: NewComposer(ComposerConfig{}), address: address}, nil
}

// Send composes msg and delivers it over one SMTP session.
//
// It shares the composition path with [smtpTransport.SendBatch] on purpose: a
// message that would be refused must be refused identically whichever call site
// it arrives through, and before a socket exists either way.
func (t *smtpTransport) Send(ctx context.Context, msg coremail.MessageValue) error {
	deliveries, failures := t.prepare([]coremail.MessageValue{msg})
	//: one message in, so at most one refusal out — and nothing was dialled.
	if len(failures) > 0 {
		//: the core verdict, unchanged.
		return failures[0]
	}
	//: one delivery, one session.
	return t.deliver(ctx, deliveries)
}

// prepare validates and composes every message, returning the deliveries that
// survived and the refusals that did not.
//
// Composition happens before any dial, so a message that will be refused costs
// no connection — which matters for a caller taking untrusted input: there is
// no message it can construct that makes this SDK open a socket.
func (t *smtpTransport) prepare(
	msgs []coremail.MessageValue,
) (deliveries []coremail.DeliveryValue, failures []error) {
	deliveries = make([]coremail.DeliveryValue, 0, len(msgs))
	failures = make([]error, 0, len(msgs))
	//: one verdict per message, and a refusal never stops the others.
	for _, msg := range msgs {
		envelope, envelopeErr := msg.Envelope()
		//: refused message: recorded and skipped, never fatal to the batch.
		if envelopeErr != nil {
			failures = append(failures, envelopeErr)
			//: next message.
			continue
		}
		raw, composeErr := t.composer.Compose(msg)
		//: same treatment for a composition refusal.
		if composeErr != nil {
			failures = append(failures, composeErr)
			//: next message.
			continue
		}
		deliveries = append(deliveries, coremail.DeliveryValue{Envelope: envelope, Raw: raw})
	}
	//: what can be sent, and what cannot.
	return deliveries, failures
}

// SendBatch composes every message first, then delivers all of them over ONE
// session — which is the whole reason this capability exists rather than
// leaving the caller to loop.
//
// Composition happens before the dial for every message, so a batch containing
// one unusable message does not open a connection at all. The delivery failures
// that follow are joined rather than short-circuited: one refused recipient in
// a batch of five hundred must not silence the rest.
func (t *smtpTransport) SendBatch(ctx context.Context, msgs []coremail.MessageValue) error {
	//: compose everything up front, so a bad message costs no connection.
	deliveries, failures := t.prepare(msgs)
	//: nothing composable means nothing to dial for.
	if len(deliveries) > 0 {
		//: one session for every message that survived composition.
		if deliverErr := t.deliver(ctx, deliveries); deliverErr != nil {
			failures = append(failures, deliverErr)
		}
	}
	//: errors.Join returns nil for an empty slice.
	return errors.Join(failures...)
}

// deliver opens one session and issues every delivery through it.
func (t *smtpTransport) deliver(ctx context.Context, deliveries []coremail.DeliveryValue) error {
	conn, dialErr := t.dial(ctx)
	//: DialFailed or TLSFailed.
	if dialErr != nil {
		//: nothing was sent.
		return dialErr
	}
	//: net/smtp has no context, so cancellation is enforced by severing the
	//: socket the client is blocked on. A Send that outlived its context would
	//: be a goroutine the caller believes it has already cancelled.
	release := watchContext(ctx, conn)
	defer release()
	client, clientErr := smtp.NewClient(conn, t.cfg.Host)
	//: GreetingFailed.
	if clientErr != nil {
		//: the greeting verdict wins; the socket's own close error is reported
		//: only if there is no verdict to report instead.
		return cmp.Or[error](wrapAs(GreetingFailed, clientErr, errs.String("stage", "greeting")), conn.Close())
	}
	current := &session{transport: t, cfg: t.cfg, client: client}
	runErr := current.run(deliveries)
	//: a session that ran to its QUIT has already closed the connection, and
	//: closing it twice reports "use of closed network connection" — which is
	//: not a failure of this send and must not be returned as one.
	if runErr == nil {
		//: accepted by the next hop. Not delivered — see coremail.Transport.
		return nil
	}
	//: an abandoned session still has to be closed, and the verdict that
	//: abandoned it is the one the caller needs.
	return cmp.Or(runErr, client.Close())
}

// run negotiates the session, issues every delivery, and closes it cleanly.
func (s *session) run(deliveries []coremail.DeliveryValue) error {
	//: EHLO, then the security policy, then the messages.
	if sessionErr := s.negotiate(); sessionErr != nil {
		//: TLSRequired, TLSFailed, AuthInsecure, AuthFailed or GreetingFailed.
		return sessionErr
	}
	//: every delivery over the one session.
	for _, delivery := range deliveries {
		//: SendRefused.
		if sendErr := s.sendOne(delivery); sendErr != nil {
			//: the session is abandoned; a server that refused one message is
			//: entitled to have changed its mind about the connection too.
			return sendErr
		}
	}
	//: QUIT, so the server learns the session ended rather than timing it out.
	if quitErr := s.client.Quit(); quitErr != nil {
		//: GreetingFailed — every message was accepted, and the caller still
		//: hears that the close was not clean.
		return wrapAs(GreetingFailed, quitErr, errs.String("stage", "quit"))
	}
	//: accepted by the next hop. Not delivered — see coremail.Transport.
	return nil
}

// negotiate runs EHLO and then the whole security policy: encryption first,
// credentials only over an encrypted session.
func (s *session) negotiate() error {
	//: EHLO before anything, so the extension list is populated.
	if helloErr := s.client.Hello(s.cfg.resolvedLocalName()); helloErr != nil {
		//: GreetingFailed.
		return wrapAs(GreetingFailed, helloErr, errs.String("stage", "ehlo"))
	}
	//: STARTTLS, when that is the configured mode.
	if s.cfg.TLS == TLSStartTLS {
		//: TLSRequired or TLSFailed.
		if tlsErr := s.startTLS(); tlsErr != nil {
			//: refused, never downgraded.
			return tlsErr
		}
	}
	//: and only then the credentials.
	return s.authenticate()
}

// startTLS upgrades the session, refusing rather than continuing when the
// server offers no upgrade.
func (s *session) startTLS() error {
	//: the advertisement is checked BEFORE the command, so the refusal names
	//: the real problem rather than reporting a 500 for an unknown verb.
	if offered, _ := s.client.Extension(extensionStartTLS); !offered {
		//: TLSRequired. This is the branch an active attacker aims for by
		//: stripping one line from the EHLO response — and it ends the session.
		return errs.Wrap(TLSRequired, errs.WrapParams{}, errs.String("host", s.cfg.Host))
	}
	//: net/smtp.StartTLS passes the config to tls.Client UNCHANGED — it does
	//: not fill ServerName — so an unnamed config would fail the handshake
	//: outright, and an InsecureSkipVerify one would succeed against anybody.
	if tlsErr := s.client.StartTLS(s.transport.tlsConfig()); tlsErr != nil {
		//: TLSFailed: a refused command, an untrusted chain or a name mismatch.
		return wrapAs(TLSFailed, tlsErr, errs.String("host", s.cfg.Host))
	}
	//: encrypted from here on.
	return nil
}

// authenticate issues AUTH, and refuses to issue it over anything unencrypted.
func (s *session) authenticate() error {
	//: no credentials configured means no AUTH command at all.
	if s.cfg.Username == "" {
		//: nothing to send.
		return nil
	}
	//: the session's ACTUAL state, not the configured intent. This is the
	//: check net/smtp will not make for us: smtp.PlainAuth sends the password
	//: in the clear whenever the server is called localhost, which in a
	//: container is every relay one hop away.
	if _, encrypted := s.client.TLSConnectionState(); !encrypted {
		//: AuthInsecure, before a single credential octet is written.
		return errs.Wrap(AuthInsecure, errs.WrapParams{},
			errs.String("host", s.cfg.Host), errs.String("tls_mode", s.cfg.TLS.String()))
	}
	//: a server that advertises no AUTH has nothing to offer the credential to.
	if offered, _ := s.client.Extension(extensionAuth); !offered {
		//: AuthFailed, again before anything is written.
		return errs.Wrap(AuthFailed, errs.WrapParams{},
			errs.String("host", s.cfg.Host), errs.String("problem", "server advertised no AUTH extension"))
	}
	//: PLAIN over an encrypted session (RFC 4954 §4). The identity is empty:
	//: an authorisation identity different from the authentication one is a
	//: delegation nobody configures by accident.
	if authErr := s.client.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); authErr != nil {
		//: AuthFailed. The cause is the server's reply; the password is not in
		//: it, because net/smtp does not echo the credential it sent.
		return wrapAs(AuthFailed, authErr, errs.String("host", s.cfg.Host))
	}
	//: authenticated.
	return nil
}

// sendOne issues MAIL, every RCPT, and DATA for a single delivery.
func (s *session) sendOne(delivery coremail.DeliveryValue) error {
	//: the return path first (RFC 5321 §3.3).
	if mailErr := s.client.Mail(delivery.Envelope.From); mailErr != nil {
		//: SendRefused; the address is the caller's own and travels as a field.
		return wrapAs(SendRefused, mailErr, errs.String("command", "MAIL"))
	}
	//: one RCPT per recipient, Bcc included — this is where a blind recipient
	//: exists, and it exists nowhere in the bytes that follow.
	for index, recipient := range delivery.Envelope.To {
		//: SendRefused. The position is diagnostic; the address is not in the
		//: Public, because a Bcc recipient is a secret of the message.
		if rcptErr := s.client.Rcpt(recipient); rcptErr != nil {
			//: refused.
			return wrapAs(SendRefused, rcptErr, errs.String("command", "RCPT"), errs.Int("recipient", index))
		}
	}
	writer, dataErr := s.client.Data()
	//: SendRefused.
	if dataErr != nil {
		//: the server declined the DATA phase.
		return wrapAs(SendRefused, dataErr, errs.String("command", "DATA"))
	}
	//: net/smtp's dataCloser dot-stuffs, so a body line of "." cannot end the
	//: message early (RFC 5321 §4.5.2).
	if _, writeErr := writer.Write(delivery.Raw); writeErr != nil {
		//: SendRefused: the write failed mid-message.
		return wrapAs(SendRefused, writeErr, errs.String("command", "DATA"))
	}
	//: Close sends the terminating dot and reads the server's verdict.
	if closeErr := writer.Close(); closeErr != nil {
		//: SendRefused: this is where a size limit or a content filter speaks.
		return wrapAs(SendRefused, closeErr, errs.String("command", "DATA-END"))
	}
	//: accepted by this hop.
	return nil
}

// dial opens the TCP connection and, for implicit TLS, completes the handshake
// before any SMTP byte is exchanged.
func (t *smtpTransport) dial(ctx context.Context) (conn net.Conn, err error) {
	dialer := net.Dialer{Timeout: t.cfg.resolvedDialTimeout()}
	raw, dialErr := dialer.DialContext(ctx, "tcp", t.address)
	//: DialFailed.
	if dialErr != nil {
		//: the host is the caller's own configuration and is safe to report.
		return nil, wrapAs(DialFailed, dialErr, errs.String("address", t.address))
	}
	//: STARTTLS and cleartext both continue on the raw socket.
	if t.cfg.TLS != TLSImplicit {
		//: the connection as dialled.
		return raw, nil
	}
	//: implicit TLS: the handshake happens before the greeting, so there is no
	//: plaintext phase at all (RFC 8314 §3.3).
	secure := tls.Client(raw, t.tlsConfig())
	//: the handshake is bounded by the caller's context, not by a second timer.
	if handshakeErr := secure.HandshakeContext(ctx); handshakeErr != nil {
		//: the socket is closed here because no client owns it yet; the
		//: handshake verdict wins, and the close error is reported only if
		//: there is somehow no handshake error to report instead.
		closed := cmp.Or[error](wrapAs(TLSFailed, handshakeErr, errs.String("address", t.address)), raw.Close())
		//: TLSFailed.
		return nil, closed
	}
	//: encrypted from the first octet.
	return secure, nil
}

// tlsConfig derives the client configuration from the identity, filling in the
// verified name when the identity names none.
//
// The fill is load-bearing. net/smtp.StartTLS hands the config to tls.Client
// exactly as given, so an empty ServerName makes the handshake fail with "either
// ServerName or InsecureSkipVerify must be specified" — and the obvious way out
// of that message, setting InsecureSkipVerify, accepts a certificate from
// anybody. Naming the configured host is the correct third option.
func (t *smtpTransport) tlsConfig() *tls.Config {
	//: a fresh config per call: core/net clones the certificates and the trust
	//: pool, so mutating this one cannot reach any other user of the identity.
	cfg := t.cfg.Identity.ClientConfig()
	//: the identity's own name wins when it has one — a caller connecting to
	//: an IP whose certificate names a host needs exactly that override.
	if cfg.ServerName == "" {
		//: the configured host, which is also the name EHLO was answered by.
		cfg.ServerName = t.cfg.Host
	}
	//: verified against a real name.
	return cfg
}

// watchContext severs conn when ctx ends, and returns the function that stops
// watching.
//
// This is how a context reaches a client that has none. It is deliberately a
// severance and not a cancellation: net/smtp is blocked in a socket read, and
// the only thing that unblocks it is the socket becoming unreadable. The
// returned release must run before the connection is closed normally, or the
// goroutine outlives the send.
func watchContext(ctx context.Context, conn net.Conn) (release func()) {
	//: a context that can never end needs no goroutine at all.
	if ctx.Done() == nil {
		//: nothing to release.
		return func() {}
	}
	//: a deadline the socket itself can enforce, so a cancellation that arrives
	//: between two reads is still noticed. A connection that refuses one is not
	//: fatal: the goroutine below is the mechanism that actually stops the send,
	//: and this is the belt beside it.
	if deadline, ok := ctx.Deadline(); ok {
		//: best effort, deliberately: a connection that refuses a deadline is
		//: still stopped by the goroutine below, which needs no deadline.
		setDeadlineBestEffort(conn, deadline)
	}
	done := make(chan struct{})
	//: LIFECYCLE: one goroutine per session. It ends when the context ends or
	//: when release() closes done, whichever happens first, and release is
	//: deferred by the only caller — so it cannot outlive the send.
	go func() {
		select {
		case <-ctx.Done():
			//: net.Conn is safe to close from another goroutine, and closing is
			//: what unblocks the read net/smtp is sitting in. The error has no
			//: caller: the send is already failing with the context's own.
			closeBestEffort(conn)
		case <-done:
			//: the session finished first.
		}
	}()
	//: idempotent enough for one deferred call per session.
	return func() { close(done) }
}

// setDeadlineBestEffort applies a socket deadline and swallows a refusal.
//
// It is a named function rather than an inline "_ =" so that the swallowing is
// a documented decision at one place: the goroutine in [watchContext] is the
// mechanism that actually stops a send, and this deadline is the belt beside
// it. A connection that has no deadline to set is still severed.
func setDeadlineBestEffort(conn net.Conn, deadline time.Time) {
	//: the caller has already decided this cannot fail the send.
	if deadlineErr := conn.SetDeadline(deadline); deadlineErr != nil {
		//: deliberately unreported: there is no caller left to report to and
		//: the goroutine covers the same ground.
		return
	}
}

// closeBestEffort severs a connection and swallows a refusal, for the paths
// where the caller is already failing with a better error than a close.
func closeBestEffort(conn net.Conn) {
	//: closing is what unblocks the read net/smtp is sitting in; whether the
	//: close itself succeeded changes nothing the caller can act on.
	if closeErr := conn.Close(); closeErr != nil {
		//: deliberately unreported.
		return
	}
}
