// Package mail — range 0.2.31.* (ADR 0064 core/mail block).
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
