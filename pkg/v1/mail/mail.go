//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/mail .

// Package mail is the public facade for the SDK's electronic-mail domain:
// build a message as a value, and hand it to a transport that refuses to lie
// about what it sent.
//
//	transport, err := mail.NewSMTP(mail.SMTPConfig{
//		Host: "smtp.example.com",
//		Port: 587,
//		TLS:  mail.TLSStartTLS, // the zero value is refused, on purpose
//		Username: "postmaster@example.com",
//		Password: secret,
//	})
//	if err != nil {
//		return err
//	}
//
//	err = transport.Send(ctx, mail.Message{
//		From:    mail.Address{Name: "Ops", Addr: "ops@example.com"},
//		To:      []mail.Address{{Addr: "user@example.net"}},
//		Bcc:     []mail.Address{{Addr: "audit@example.com"}}, // envelope only
//		Subject: "Réunion à 9h",                              // encoded per RFC 2047
//		Text:    "Bonjour,",
//		HTML:    "<p>Bonjour,</p>",                           // → multipart/alternative
//	})
//
// # Header injection is refused, never repaired
//
// A header field ends at CRLF (RFC 5322 §2.2). So a carriage return inside a
// subject, a display name or a filename does not corrupt that field — it ENDS
// it, and everything after it becomes headers the caller never wrote. "Bcc:
// attacker@example.com" is one of them, and the message is then delivered to
// somebody the sender cannot see in what it composed.
//
// This package REFUSES such a value with [HeaderInjection]. It does not clean
// it up, and the reason is that the standard library will: mime.QEncoding
// turns the CRLF into "=0D=0A", mime.FormatMediaType percent-escapes it, and
// net/mail.Address.String() passes it through untouched inside the address. Two
// of those produce a message that is not the one the caller wrote while
// reporting success, and the third produces the injection itself.
//
// The error names the FIELD and never the value — the value is by construction
// the string an attacker chose, and a Public message travels to strangers. The
// diagnosis lives in the log-only fields.
//
// # Bcc reaches the envelope and never a header
//
// RFC 5322 §3.6.3 permits three treatments of a Bcc field. This package takes
// the only one that cannot leak: the addresses become RCPT TO commands and no
// header names them. [Message.Envelope] is where that happens, and the
// composed bytes are asserted not to contain a blind recipient.
//
// # The MIME structure follows from the fields
//
//	text only                    → text/plain
//	HTML only                    → text/html
//	text + HTML                  → multipart/alternative(plain, html)
//	body + inline parts          → multipart/related(body, inline…)
//	body + attachments           → multipart/mixed(body, attachment…)
//	body + inline + attachments  → multipart/mixed(multipart/related(…), attachment…)
//
// An [Attachment] carrying a ContentID is inline and referenceable from the
// HTML as cid:<ContentID> (RFC 2392 §2); one without is a file. Inside
// multipart/alternative the plain part comes FIRST, because RFC 2046 §5.1.4
// orders alternatives by increasing preference — writing HTML first asks every
// client to display the plain text.
//
// # TLS is a decision, and its zero value is refused
//
// [SMTPConfig.TLS] has no default. Choosing encryption silently would break
// every caller pointing at a plaintext relay on a private segment; choosing
// none silently would ship everyone else's credentials in the clear. So the
// zero value returns [InvalidConfig] at construction and the caller writes
// [TLSStartTLS], [TLSImplicit] or [TLSDisabled].
//
// There is deliberately no opportunistic mode. A transport that encrypts when
// the server offers it and continues in the clear when it does not is one
// stripped EHLO line away from handing an attacker the whole session — so a
// server that does not advertise STARTTLS gets [TLSRequired] and no message.
//
// Credentials never travel unencrypted. [TLSDisabled] with a Username is
// refused at construction, and an unencrypted session is refused again before
// the AUTH command — which is not redundant, because net/smtp's own PlainAuth
// sends the password in the clear whenever the server is called localhost, and
// in a container that is every relay one hop away.
//
// # What this package does NOT promise
//
// A nil error from [Transport.Send] means the next hop ACCEPTED the message.
// It is not delivery: SMTP accepts responsibility hop by hop (RFC 5321 §6.1),
// and the hop that eventually refuses says so in a bounce, hours later, to the
// envelope's return path.
//
// It is not deliverability either. Whether a message reaches an inbox rather
// than a spam folder depends on SPF, DKIM and DMARC — records in DNS and a
// signing key — which are infrastructure decisions with an operational
// lifetime, not a library call. This package signs nothing and will not
// pretend the absence of a signature is a detail.
//
// And it is not rendering. Whether a client displays the HTML, the plain text,
// or an inline image at all is that client's decision; what this package
// guarantees is that the structure is the one the RFCs prescribe for the
// content supplied.
//
// # Testing
//
// [NewMemory] returns a transport that COMPOSES every message and records the
// bytes instead of dialling. It is a double rather than a stub: it runs the
// same composer and the same guards, so a message production would refuse is
// refused in the test that exists to catch it.
//
//	box := mail.NewMemory()
//	_ = service.Notify(ctx, box)      // the code under test
//	sent := box.Sent()                // envelope + composed the RFC 5322 wire form
package mail

import (
	coremail "github.com/kitsunium/sdk/internal/core/mail"
	svcmail "github.com/kitsunium/sdk/internal/service/mail"
)

// TLSUnset is the zero value and is refused at construction — neither
// "encrypt" nor "do not" is a guess this SDK is entitled to make for a caller.
const TLSUnset TLSMode = svcmail.TLSUnset

// TLSStartTLS requires the server to advertise STARTTLS (RFC 3207) and refuses
// the session when it does not. The port-587 shape.
//
// There is deliberately no opportunistic variant: a transport that encrypts
// when the server offers it and continues in the clear when it does not is one
// stripped EHLO line away from handing an attacker the whole session.
const TLSStartTLS TLSMode = svcmail.TLSStartTLS

// TLSImplicit negotiates TLS before the greeting, which RFC 8314 §3.3 prefers
// because there is no plaintext phase to interfere with. The port-465 shape.
const TLSImplicit TLSMode = svcmail.TLSImplicit

// TLSDisabled sends in the clear and must be spelled out loud. Credentials are
// refused with it, at construction.
const TLSDisabled TLSMode = svcmail.TLSDisabled

// Message is the public alias for one mail, as a value.
type Message = coremail.MessageValue

// Address is the public alias for one RFC 5322 mailbox.
type Address = coremail.AddressValue

// Attachment is the public alias for one file or inline part. A non-empty
// ContentID makes it inline.
type Attachment = coremail.AttachmentValue

// HeaderField is the public alias for one additional header. It is a slice
// element rather than a map entry so a composed message is deterministic and so
// a field may legitimately repeat.
type HeaderField = coremail.HeaderFieldValue

// Envelope is the public alias for the SMTP envelope: one MAIL FROM and every
// RCPT TO, Bcc included.
type Envelope = coremail.EnvelopeValue

// Delivery is the public alias for one record of what a transport put on the
// wire: the envelope, and the composed bytes.
type Delivery = coremail.DeliveryValue

// Transport is the public alias for the frozen port. A new capability arrives
// as a sibling interface reached by type assertion, never as a second method
// (ADR 0039).
type Transport = coremail.Transport

// BatchSender is the public alias for the sibling that sends several messages
// over one session.
type BatchSender = coremail.BatchSender

// Outbox is the public alias for the sibling that exposes what was sent. Only
// the in-memory transport implements it.
type Outbox = coremail.Outbox

// FullTransport is the public alias for the union [NewMemory] returns. A
// parameter should still ask for the narrowest thing it uses.
type FullTransport = coremail.FullTransport

// SMTPConfig is the public alias for the SMTP transport's configuration.
type SMTPConfig = svcmail.SMTPConfig

// TLSMode is the public alias for the encryption mode. Its zero value is refused.
type TLSMode = svcmail.TLSMode

// ComposerConfig is the public alias for the composer's optional clock and
// randomness source. Both clamp rather than refuse.
type ComposerConfig = svcmail.ComposerConfig

// Composer is the public alias for the type that turns a [Message] into
// the RFC 5322 wire form without sending anything.
type Composer = svcmail.Composer

var (
	// HeaderInjection is returned when a header name or value carries CR, LF or
	// NUL. It is a refusal and never a repair, and it names the field rather
	// than the value.
	HeaderInjection = coremail.HeaderInjection
	// InvalidAddress is returned for an addr-spec outside the dot-atom subset
	// of RFC 5322 §3.4.1 this domain accepts, or outside the RFC 5321 §4.5.3.1
	// length limits.
	InvalidAddress = coremail.InvalidAddress
	// UnsupportedAddress is returned for a non-ASCII addr-spec (RFC 6531
	// SMTPUTF8) or a quoted local part — both legal mail, both refused by name
	// rather than transliterated into a deliverable but different address.
	UnsupportedAddress = coremail.UnsupportedAddress
	// MissingSender is returned for a message with no From mailbox.
	MissingSender = coremail.MissingSender
	// NoRecipients is returned when To, Cc and Bcc are all empty, before any
	// socket is opened.
	NoRecipients = coremail.NoRecipients
	// EmptyBody is returned for a message with no text, no HTML and no
	// attachment.
	EmptyBody = coremail.EmptyBody
	// ReservedHeader is returned when Headers names a field the composer owns —
	// including Bcc, which is the injection arriving through the front door.
	ReservedHeader = coremail.ReservedHeader
	// InvalidAttachment is returned for an attachment with no filename, an
	// unparseable media type, a malformed Content-ID, or an inline part in a
	// message with no body to reference it.
	InvalidAttachment = coremail.InvalidAttachment
	// HeaderTooLong is returned when a header line exceeds the RFC 5322 §2.1.1
	// limit of 998 octets and offers no whitespace to fold at. It is refused
	// rather than truncated.
	HeaderTooLong = coremail.HeaderTooLong
	// ComposeFailed is returned when the MIME body could not be assembled.
	ComposeFailed = svcmail.ComposeFailed
	// InvalidConfig is returned by [NewSMTP] for a configuration that cannot be
	// honoured as written — including a [TLSUnset] mode.
	InvalidConfig = svcmail.InvalidConfig
	// DialFailed is returned when the server could not be reached, or the
	// context ended before the greeting.
	DialFailed = svcmail.DialFailed
	// GreetingFailed is returned when the connection opened and the SMTP
	// conversation did not.
	GreetingFailed = svcmail.GreetingFailed
	// TLSRequired is returned when STARTTLS was required and the server never
	// offered it. The session is ended, never downgraded.
	TLSRequired = svcmail.TLSRequired
	// TLSFailed is returned when the handshake or the STARTTLS command failed.
	TLSFailed = svcmail.TLSFailed
	// AuthInsecure is returned when credentials would have travelled over an
	// unencrypted session — at construction, and again before AUTH.
	AuthInsecure = svcmail.AuthInsecure
	// AuthFailed is returned when the server rejected the credentials or
	// advertised no AUTH mechanism.
	AuthFailed = svcmail.AuthFailed
	// SendRefused is returned when MAIL, RCPT or DATA was answered with a
	// failure reply. The server's own text travels as a log-only field.
	SendRefused = svcmail.SendRefused
)

// NewSMTP returns a transport that delivers over SMTP, refusing at construction
// any configuration it could not honour — an unset TLS mode, an out-of-range
// port, or credentials it would have to send in the clear.
//
// It holds no pooled connection: it dials per send, and a caller that wants one
// session for many messages asks for it through [BatchSender].
func NewSMTP(cfg SMTPConfig) (transport Transport, err error) {
	//: delegate to the service constructor.
	return svcmail.NewSMTP(cfg)
}

// NewMemory returns a transport that composes every message and records the
// result instead of dialling anything — the double a consumer's tests wire in
// place of [NewSMTP].
//
// It takes no arguments on purpose: every knob it could offer is one a test has
// to set before it can assert anything, and the value of a double is that it
// costs one line.
func NewMemory() FullTransport {
	//: delegate to the service constructor.
	return svcmail.NewMemory()
}

// NewComposer returns a composer that renders a [Message] to the RFC 5322 wire form
// without sending it — for a caller that hands the bytes to something this SDK
// does not implement, or that wants a golden test over its own templates.
//
// A [ComposerConfig] with a fixed clock and a fixed randomness source makes
// composition byte-deterministic.
func NewComposer(cfg ComposerConfig) *Composer {
	//: delegate to the service constructor.
	return svcmail.NewComposer(cfg)
}

// Compose renders msg to the RFC 5322 wire form using the system clock and
// crypto/rand. It is the one-line form of [NewComposer].
func Compose(msg Message) (raw []byte, err error) {
	//: a fresh composer per call: it holds no state between messages.
	return svcmail.NewComposer(ComposerConfig{}).Compose(msg)
}

// Validate reports whether msg can be composed and sent, returning the first
// typed refusal it finds.
//
// It is exported so a caller can reject a message at the edge — where a form
// was submitted — rather than at the transport, and get the same verdict either
// way.
func Validate(msg Message) error {
	//: the same guard both transports run.
	return coremail.Validate(msg)
}
