// Package mail — declares the sentinel *errs.Error port outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public string here carries a header VALUE, an address, a subject or an
// attachment's bytes. A Public is read by third parties — it lands in an HTTP
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
// exit statuses deserves to tell the two apart.
const exitNoPerm int = 77

// exitUnavailable matches sysexits EX_UNAVAILABLE (69). The message names a
// capability this domain does not offer; retrying changes nothing.
const exitUnavailable int = 69

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
		"core/mail: header name or value contains CR, LF or NUL — RFC 5322 §2.2 ends a field at CRLF, so the remainder would become headers the caller never wrote; refused, never sanitised",
		errs.WithExitCode(exitNoPerm))

	// InvalidAddress is returned for an addr-spec outside the dot-atom subset
	// of RFC 5322 §3.4.1 this domain accepts, or outside the RFC 5321 §4.5.3.1
	// octet limits (64 for the local part, 255 for the domain).
	InvalidAddress = errs.Define(CodeInvalidAddress, "INVALID_ADDRESS",
		"A mail address in that message is not usable",
		"core/mail: addr-spec is empty, has no single @, carries a space, angle bracket or control byte, or exceeds the RFC 5321 §4.5.3.1 length limits",
		errs.WithExitCode(exitDataErr))

	// MissingSender is returned for a Message whose From carries no address.
	MissingSender = errs.Define(CodeMissingSender, "MISSING_SENDER",
		"A message must name the mailbox it is from",
		"core/mail: Message.From is the zero Address; RFC 5322 §3.6 makes the originator field mandatory and there is no defensible default",
		errs.WithExitCode(exitDataErr))

	// NoRecipients is returned when To, Cc and Bcc are all empty.
	//
	// The refusal happens before any socket is opened. An SMTP session with no
	// RCPT TO cannot succeed, so the only thing dialling first would buy is a
	// slower, less specific failure.
	NoRecipients = errs.Define(CodeNoRecipients, "NO_RECIPIENTS",
		"A message must name at least one recipient",
		"core/mail: To, Cc and Bcc are all empty, so the SMTP session would have no RCPT TO to issue",
		errs.WithExitCode(exitDataErr))

	// EmptyBody is returned for a Message with no text, no HTML and no
	// attachment — ADR 0031's refusing half, applied to content.
	EmptyBody = errs.Define(CodeEmptyBody, "EMPTY_BODY",
		"A message must carry text, HTML or an attachment",
		"core/mail: Text, HTML and Attachments are all empty; a zero body is an unfilled struct rather than a decision to send nothing",
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
		"core/mail: Headers names a composer-owned field (From, To, Cc, Bcc, Reply-To, Subject, Date, Message-ID, MIME-Version or a Content-* field)",
		errs.WithExitCode(exitDataErr))

	// InvalidAttachment is returned for an attachment the composer cannot
	// render: no filename, an unparseable media type, a malformed Content-ID,
	// or an inline part in a message with no body for its cid: to be
	// referenced from.
	InvalidAttachment = errs.Define(CodeInvalidAttachment, "INVALID_ATTACHMENT",
		"An attachment on that message is not usable",
		"core/mail: attachment has no filename, an unparseable media type, a Content-ID outside the RFC 2392 §2 grammar, or is inline in a message with no text or HTML body to reference it",
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
		"core/mail: a header line exceeds the RFC 5322 §2.1.1 limit of 998 octets and contains no whitespace to fold at; refused rather than truncated",
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
		"core/mail: non-ASCII addr-spec (RFC 6531 SMTPUTF8) or quoted local-part; refused by name rather than transliterated into a deliverable but different address",
		errs.WithExitCode(exitUnavailable))
)
