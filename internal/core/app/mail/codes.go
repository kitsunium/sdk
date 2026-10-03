// Package mail — ranges 0.2.31.* (ADR 0064 core/app/mail block) and 0.3.61.*
// (ADR 0064 service/app/mail block, declared here since ADR 0160).
package mail

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.31.0 - 0.2.31.255

// CodeHeaderInjection identifies a header name or value carrying CR, LF or
// NUL. It is THE failure of this domain: RFC 5322 §2.2 terminates a field with
// CRLF, so those two octets do not corrupt a header, they end it and begin one
// the caller never wrote.
const CodeHeaderInjection errs.Code = 0x00_02_1F_01 // 0.2.31.1

// CodeInvalidAddress identifies an addr-spec outside the dot-atom subset of
// RFC 5322 §3.4.1 that this domain accepts: empty, missing or repeating "@",
// carrying a space or an angle bracket, or exceeding the RFC 5321 §4.5.3.1
// octet limits.
const CodeInvalidAddress errs.Code = 0x00_02_1F_02 // 0.2.31.2

// CodeMissingSender identifies a Message with no From mailbox. RFC 5322 §3.6
// makes the originator field mandatory, and an empty From is what an unfilled
// struct looks like.
const CodeMissingSender errs.Code = 0x00_02_1F_03 // 0.2.31.3

// CodeNoRecipients identifies a Message whose To, Cc and Bcc are all empty.
// SMTP has no RCPT TO to issue, so the send could only ever fail — it fails
// here instead, before a socket is opened.
const CodeNoRecipients errs.Code = 0x00_02_1F_04 // 0.2.31.4

// CodeEmptyBody identifies a Message with no text, no HTML and no attachment.
// A zero body is not "send an empty mail", it is a struct nobody filled in.
const CodeEmptyBody errs.Code = 0x00_02_1F_05 // 0.2.31.5

// CodeReservedHeader identifies an extra header whose name the composer owns.
// Letting one through would either emit the field twice — RFC 5322 §3.6 allows
// exactly one From, Date or Subject — or let a caller spell Bcc by hand, which
// is the injection attack with the SDK's own cooperation.
const CodeReservedHeader errs.Code = 0x00_02_1F_06 // 0.2.31.6

// CodeInvalidAttachment identifies an attachment the composer cannot render:
// no filename, an unparseable media type, an inline part with no body to be
// referenced from, or a Content-ID outside the addr-spec grammar RFC 2392 §2
// borrows for cid: URLs.
const CodeInvalidAttachment errs.Code = 0x00_02_1F_07 // 0.2.31.7

// CodeHeaderTooLong identifies a header line that cannot be folded under the
// RFC 5322 §2.1.1 hard limit of 998 octets. Folding is only legal at existing
// whitespace (§2.2.3), so a single token longer than the limit has no legal
// fold point and is refused rather than cut.
const CodeHeaderTooLong errs.Code = 0x00_02_1F_08 // 0.2.31.8

// CodeUnsupportedAddress identifies a syntactically plausible address this
// domain deliberately does not carry: a non-ASCII addr-spec (RFC 6531
// SMTPUTF8) or a quoted local-part. Both are legal mail and neither is
// supported, so they are named rather than mangled into something deliverable
// to the wrong mailbox.
const CodeUnsupportedAddress errs.Code = 0x00_02_1F_09 // 0.2.31.9

// range: 0.3.61.0 - 0.3.61.255
//
// The outcomes of composition and of the SMTP session. The range was allocated
// to internal/service/app/mail, which raises these codes, and it is declared
// here with the message refusals so that every code of the domain is in one
// place (ADR 0160). A code keeps the value its allocation gave it whichever
// layer declares it, so the layer byte still reads 3.

// CodeComposeFailed identifies a MIME body the composer could not assemble:
// an encoder that refused a byte, or a media type mime.ParseMediaType would
// not accept.
const CodeComposeFailed errs.Code = 0x00_03_3D_01 // 0.3.61.1

// CodeInvalidConfig identifies an SMTP transport that cannot be constructed as
// written: no host, a port outside 1-65535, or — the one that matters — a
// TLSMode left at its zero value.
const CodeInvalidConfig errs.Code = 0x00_03_3D_02 // 0.3.61.2

// CodeDialFailed identifies a TCP connection that was never established.
const CodeDialFailed errs.Code = 0x00_03_3D_03 // 0.3.61.3

// CodeGreetingFailed identifies a server that answered the connection but not
// the SMTP conversation: a refused greeting, a refused EHLO, or a QUIT the
// server never acknowledged.
const CodeGreetingFailed errs.Code = 0x00_03_3D_04 // 0.3.61.4

// CodeTLSRequired identifies a session that could not be encrypted because the
// server never offered STARTTLS. It is a REFUSAL and never a downgrade.
const CodeTLSRequired errs.Code = 0x00_03_3D_05 // 0.3.61.5

// CodeTLSFailed identifies a TLS handshake or STARTTLS command the server or
// the certificate chain refused.
const CodeTLSFailed errs.Code = 0x00_03_3D_06 // 0.3.61.6

// CodeAuthInsecure identifies credentials that would have travelled over an
// unencrypted session. The refusal happens BEFORE the AUTH command is written.
const CodeAuthInsecure errs.Code = 0x00_03_3D_07 // 0.3.61.7

// CodeAuthFailed identifies credentials the server rejected, or a server that
// advertised no AUTH mechanism at all.
const CodeAuthFailed errs.Code = 0x00_03_3D_08 // 0.3.61.8

// CodeSendRefused identifies a MAIL, RCPT or DATA command the server answered
// with a failure reply.
const CodeSendRefused errs.Code = 0x00_03_3D_09 // 0.3.61.9

// CodeInvalidURL identifies an SMTP URL ParseURL could not read: not a URL, a
// scheme other than smtp or smtps, a path, a fragment, an unknown or repeated
// parameter, a tls mode it does not know, no host, or a port out of range.
const CodeInvalidURL errs.Code = 0x00_03_3D_0A // 0.3.61.10
