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
// header names them. [EnvelopeOf] is where that happens, and the composed
// bytes are asserted not to contain a blind recipient.
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
// [SMTPConfig].TLS has no default. Choosing encryption silently would break
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
// # A configuration from one URL, and a password nothing prints
//
// [ParseURL] reads the form a deployment hands over in one variable:
//
//	cfg, err := mail.ParseURL(os.Getenv("SMTP_URL"))
//	// smtp://user:pass@relay.example:587?tls=starttls|implicit|none
//	// smtps://user:pass@relay.example        (implicit TLS, port 465)
//
// smtp:// is STARTTLS unless the tls parameter says otherwise, smtps:// is
// implicit TLS and may only say so again, and a plaintext session is spelled
// tls=none. The port defaults by mode (587, 465, 25). The result is checked as
// [NewSMTP] checks it, so credentials with tls=none fail here with
// [AuthInsecure]. A refusal is [InvalidURL] with a clause, and it never quotes
// the URL — its userinfo is the password.
//
// An [SMTPConfig] never renders its password: every fmt verb (it implements
// Format), String, GoString and its JSON write "<redacted>" where the password
// is, and the empty string where none is set.
//
// # What this package does NOT promise
//
// A nil error from [Transport].Send means the next hop ACCEPTED the message.
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
// # A durable outbox
//
// A transport sends once, synchronously, and a relay that is down makes that
// the caller's problem. The outbox that retries on a backoff, dead-letters
// with the last failure and keeps a Message-ID stable across retries is
// package [github.com/kitsunium/sdk/pkg/v1/app/mail/spool]: it stands on the
// queue domain, so a program that only composes and sends links none of it.
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
//
// [NewCapture] is the same double keeping only the last N deliveries — the
// transport a development server delivers through, where NewMemory would keep
// every mail for as long as the server runs.
package mail
