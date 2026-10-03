// Package mail — declares the sentinel *errs.Error outcomes of the domain: the
// refusals of a message, and the outcomes of composition and of the SMTP
// session. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
//
// No Public string here carries a header VALUE, an address, a subject, an
// attachment's bytes, a credential or a server reply. A Public is read by third parties — it lands in an HTTP
// body, a queue's dead-letter record, a support ticket — and the values this
// domain refuses are, by construction, exactly the ones an attacker chose. The
// field NAME travels in Public where it helps; the value travels only as a
// log-only field, reachable through errs.FieldsOf.
package mail

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR (65). The caller handed the domain a
// message it cannot act on; the program is fine, the argument is not.
const exitDataErr int = 65

// exitNoPerm matches sysexits EX_NOPERM (77). A header-injection refusal is a
// security verdict rather than a formatting accident, and a supervisor reading
// exit statuses deserves to tell the two apart — as are a refused downgrade
// and a refused cleartext credential, which belong here and not among the I/O
// accidents.
const exitNoPerm int = 77

// exitUnavailable matches sysexits EX_UNAVAILABLE (69). The message names a
// capability this domain does not offer; retrying changes nothing.
const exitUnavailable int = 69

// exitConfig matches sysexits EX_CONFIG (78): a permanent wiring fault. The
// same call will be refused identically forever and the fix is an edit at the
// call site.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75): the far side was not
// reachable or not willing right now. Retrying is meaningful.
const exitTempFail int = 75

// exitNoHost matches sysexits EX_NOHOST (68): the named host did not answer.
const exitNoHost int = 68

var (
	// HeaderInjection is returned when a header name or value carries CR, LF
	// or NUL.
	//
	// It is a REFUSAL and never a repair. mime.QEncoding.Encode would turn the
	// CRLF into "=0D=0A" and mime.FormatMediaType would percent-escape it —
	// both produce a deliverable message that is not the one the caller wrote,
	// and both report success. A caller who learns nothing cannot fix the
	// input, and the next message carries the same payload.
	//
	// The Public names no value. "Subject" is safe to say; the string an
	// attacker put in it is not.
	HeaderInjection = errs.Define(CodeHeaderInjection, "HEADER_INJECTION",
		"A message header contains a line break and was refused",
		"core/app/mail: header name or value contains CR, LF or NUL — RFC 5322 §2.2 ends a field at CRLF, so the remainder would become headers the caller never wrote; refused, never sanitised",
		errs.WithExitCode(exitNoPerm))

	// InvalidAddress is returned for an addr-spec outside the dot-atom subset
	// of RFC 5322 §3.4.1 this domain accepts, or outside the RFC 5321 §4.5.3.1
	// octet limits (64 for the local part, 255 for the domain).
	InvalidAddress = errs.Define(CodeInvalidAddress, "INVALID_ADDRESS",
		"A mail address in that message is not usable",
		"core/app/mail: addr-spec is empty, has no single @, carries a space, angle bracket or control byte, or exceeds the RFC 5321 §4.5.3.1 length limits",
		errs.WithExitCode(exitDataErr))

	// MissingSender is returned for a Message whose From carries no address.
	MissingSender = errs.Define(CodeMissingSender, "MISSING_SENDER",
		"A message must name the mailbox it is from",
		"core/app/mail: Message.From is the zero Address; RFC 5322 §3.6 makes the originator field mandatory and there is no defensible default",
		errs.WithExitCode(exitDataErr))

	// NoRecipients is returned when To, Cc and Bcc are all empty.
	//
	// The refusal happens before any socket is opened. An SMTP session with no
	// RCPT TO cannot succeed, so the only thing dialling first would buy is a
	// slower, less specific failure.
	NoRecipients = errs.Define(CodeNoRecipients, "NO_RECIPIENTS",
		"A message must name at least one recipient",
		"core/app/mail: To, Cc and Bcc are all empty, so the SMTP session would have no RCPT TO to issue",
		errs.WithExitCode(exitDataErr))

	// EmptyBody is returned for a Message with no text, no HTML and no
	// attachment — ADR 0031's refusing half, applied to content.
	EmptyBody = errs.Define(CodeEmptyBody, "EMPTY_BODY",
		"A message must carry text, HTML or an attachment",
		"core/app/mail: Text, HTML and Attachments are all empty; a zero body is an unfilled struct rather than a decision to send nothing",
		errs.WithExitCode(exitDataErr))

	// ReservedHeader is returned when Message.Headers names a field the
	// composer owns.
	//
	// Two different failures share this refusal. Emitting From, Date or Subject
	// twice violates RFC 5322 §3.6, which permits exactly one of each, and
	// leaves every receiver free to pick a different one. And spelling "Bcc"
	// by hand is the injection this domain exists to stop, arriving through the
	// front door instead.
	ReservedHeader = errs.Define(CodeReservedHeader, "RESERVED_HEADER",
		"That header is set by the mail composer and cannot be overridden",
		"core/app/mail: Headers names a composer-owned field (From, To, Cc, Bcc, Reply-To, Subject, Date, Message-ID, MIME-Version or a Content-* field)",
		errs.WithExitCode(exitDataErr))

	// InvalidAttachment is returned for an attachment the composer cannot
	// render: no filename, an unparseable media type, a malformed Content-ID,
	// or an inline part in a message with no body for its cid: to be
	// referenced from.
	InvalidAttachment = errs.Define(CodeInvalidAttachment, "INVALID_ATTACHMENT",
		"An attachment on that message is not usable",
		"core/app/mail: attachment has no filename, an unparseable media type, a Content-ID outside the RFC 2392 §2 grammar, or is inline in a message with no text or HTML body to reference it",
		errs.WithExitCode(exitDataErr))

	// HeaderTooLong is returned when a header line cannot be folded under the
	// RFC 5322 §2.1.1 hard limit of 998 octets.
	//
	// Folding inserts CRLF before existing whitespace (§2.2.3) — unfolding
	// removes the CRLF and keeps the whitespace, so the value survives exactly.
	// A token with no whitespace in it has no legal fold point, and truncating
	// it would deliver a different value while reporting success.
	HeaderTooLong = errs.Define(CodeHeaderTooLong, "HEADER_TOO_LONG",
		"A message header is too long to fold and was refused",
		"core/app/mail: a header line exceeds the RFC 5322 §2.1.1 limit of 998 octets and contains no whitespace to fold at; refused rather than truncated",
		errs.WithExitCode(exitDataErr))

	// UnsupportedAddress is returned for a non-ASCII addr-spec (RFC 6531
	// SMTPUTF8) or a quoted local-part.
	//
	// Both are legal mail. Neither is carried here, and saying so BY NAME is
	// the point: SMTPUTF8 must be negotiated with the server, net/smtp does
	// not negotiate it, and the alternative to refusing is punycoding a domain
	// or dropping accents from a local part — which delivers, silently, to a
	// different mailbox.
	UnsupportedAddress = errs.Define(CodeUnsupportedAddress, "UNSUPPORTED_ADDRESS",
		"That address form is not supported by this mail transport",
		"core/app/mail: non-ASCII addr-spec (RFC 6531 SMTPUTF8) or quoted local-part; refused by name rather than transliterated into a deliverable but different address",
		errs.WithExitCode(exitUnavailable))

	// The outcomes of composition and of the SMTP session, raised by
	// internal/service/app/mail. They are declared here, with the message
	// refusals, so that the domain's codes and sentinels are in one place
	// (ADR 0160). A server's reply text is third-party data of unbounded length
	// that this SDK did not write; it travels as a log-only field and never into
	// a message a caller might render.

	// ComposeFailed is returned when the MIME body could not be assembled.
	//
	// It is deliberately rare: everything a caller can get wrong is refused by
	// the message guards with a specific verdict, so reaching this one means
	// an encoder or a media-type parser refused something those guards
	// accepted — which is a defect in the composer, not in the caller's
	// message.
	ComposeFailed = errs.Define(CodeComposeFailed, "COMPOSE_FAILED",
		"The message could not be assembled into a MIME body",
		"service/app/mail: a MIME encoder, a multipart writer or mime.ParseMediaType refused input the core/app/mail guards accepted — the cause travels as a field",
		errs.WithExitCode(exitDataErr))

	// InvalidConfig is returned by the SMTP constructor for a configuration
	// that cannot be honoured as written.
	//
	// The zero TLSMode is the interesting member. It is REFUSED rather than
	// read as a default, because both candidate defaults are wrong: choosing
	// encryption silently breaks every caller pointing at a plaintext relay on
	// a private network, and choosing none silently ships credentials in the
	// clear for everyone else. ADR 0031's refusing half, applied where the
	// SDK genuinely cannot know.
	InvalidConfig = errs.Define(CodeInvalidConfig, "INVALID_CONFIG",
		"The SMTP transport configuration is not usable as written",
		"service/app/mail: empty Host, Port outside 1-65535, or TLS left at its zero value — the zero is refused because neither 'encrypt' nor 'do not' is a safe guess (ADR 0031)",
		errs.WithExitCode(exitConfig))

	// DialFailed is returned when the TCP connection was never established, or
	// was severed by the caller's context before the greeting.
	DialFailed = errs.Define(CodeDialFailed, "DIAL_FAILED",
		"The mail server could not be reached",
		"service/app/mail: TCP dial failed, timed out, or the context was cancelled before the SMTP greeting; the dial error travels as a field",
		errs.WithExitCode(exitNoHost))

	// GreetingFailed is returned when the connection opened and the SMTP
	// conversation did not: a refused greeting, a refused EHLO, a QUIT the
	// server never acknowledged.
	GreetingFailed = errs.Define(CodeGreetingFailed, "GREETING_FAILED",
		"The mail server did not accept the SMTP conversation",
		"service/app/mail: the 220 greeting, the EHLO/HELO exchange or the closing QUIT was refused or malformed; the server reply travels as a field",
		errs.WithExitCode(exitTempFail))

	// TLSRequired is returned when the transport is configured for STARTTLS
	// and the server never advertised it.
	//
	// This is the defect the domain refuses to have. "Opportunistic STARTTLS"
	// — encrypt if offered, continue in the clear if not — is the shape almost
	// every mail library ships, and it means an active attacker strips the
	// advertisement and reads everything, including the credentials, while the
	// caller's logs show a successful send. There is no configuration here
	// that produces that behaviour, and adding one is not a feature request.
	TLSRequired = errs.Define(CodeTLSRequired, "TLS_REQUIRED",
		"The mail server did not offer the encryption this transport requires",
		"service/app/mail: TLSStartTLS was configured and the EHLO response advertised no STARTTLS extension; refused, never downgraded to cleartext",
		errs.WithExitCode(exitNoPerm))

	// TLSFailed is returned when STARTTLS or the implicit handshake was
	// attempted and did not complete: a refused command, an untrusted chain, a
	// name that does not match.
	TLSFailed = errs.Define(CodeTLSFailed, "TLS_FAILED",
		"The connection to the mail server could not be encrypted",
		"service/app/mail: the STARTTLS command was refused, or the TLS handshake failed on trust, name or version; the handshake error travels as a field",
		errs.WithExitCode(exitNoPerm))

	// AuthInsecure is returned when credentials were configured for a session
	// that is not encrypted — at CONSTRUCTION when TLS is disabled outright,
	// and again before the AUTH command when the session turned out not to be
	// encrypted after all.
	//
	// The second check is not redundant, and net/smtp is the reason. Its
	// PlainAuth carves out an exception for a server named localhost —
	// "isLocalhost(server.Name)" — and sends the password in the clear to it.
	// That is defensible for the stdlib's audience and indefensible for a
	// transport in a container, where the relay one hop away IS 127.0.0.1 and
	// the network between them is a bridge somebody else can join.
	AuthInsecure = errs.Define(CodeAuthInsecure, "AUTH_INSECURE",
		"Credentials cannot be sent over an unencrypted mail session",
		"service/app/mail: Username is set on a session that is not encrypted; refused before the AUTH command is written, including for a localhost relay where net/smtp.PlainAuth would have sent it",
		errs.WithExitCode(exitNoPerm))

	// AuthFailed is returned when the server rejected the credentials, or
	// advertised no AUTH mechanism to offer them through.
	AuthFailed = errs.Define(CodeAuthFailed, "AUTH_FAILED",
		"The mail server rejected the credentials",
		"service/app/mail: the AUTH exchange was refused, or the EHLO response advertised no AUTH extension; the server reply travels as a field and the credential never leaves this process's memory",
		errs.WithExitCode(exitNoPerm))

	// SendRefused is returned when MAIL, RCPT or DATA was answered with a
	// failure reply.
	//
	// It carries the SMTP reply as a log-only field, never as Public: a reply
	// is third-party text of unbounded length that may name a recipient, and
	// Public is the half that travels to strangers.
	SendRefused = errs.Define(CodeSendRefused, "SEND_REFUSED",
		"The mail server refused the message",
		"service/app/mail: MAIL FROM, RCPT TO or DATA was answered with a failure reply; the command and the server's reply travel as log-only fields",
		errs.WithExitCode(exitTempFail))

	// InvalidURL is returned by ParseURL for a URL it cannot read. It names the
	// clause that failed and NEVER the URL: an SMTP URL carries the password
	// in its userinfo, and every part of it — the host, the port, a parameter
	// name — is quoted by no refusal, because a URL that failed to parse is
	// exactly the one whose parts are not where they should be.
	InvalidURL = errs.Define(CodeInvalidURL, "INVALID_URL",
		"The SMTP URL is not usable as written",
		"service/app/mail: ParseURL refused the URL; the problem field names the clause, and the URL itself is never repeated because it carries the password",
		errs.WithExitCode(exitConfig))
)
