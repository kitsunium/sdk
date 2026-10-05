// Package mail — the mailbox value.
//
// Package mail — one file or inline part, as a value.
//
// Package mail — ranges 0.2.31.* (ADR 0064 core/app/mail block) and 0.3.61.*
// (ADR 0064 service/app/mail block, declared here since ADR 0160).
//
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
//
// Package mail — the names of the header fields the composer owns, and one
// additional header as a value.
//
// Package mail declares the electronic-mail port: the message as a VALUE, the
// envelope derived from it, and the [Transport] that carries one.
//
// The domain's whole security posture is one sentence: a header that contains
// a carriage return or a line feed is REFUSED, never repaired. RFC 5322 §2.2
// makes a header field a name, a colon and a value terminated by CRLF, so a
// CRLF inside the value does not corrupt the field — it ENDS it and starts
// another one the caller never wrote. "Bcc: attacker@example.com" is then a
// perfectly well-formed header, and the mail is delivered to a party the sender
// cannot see in what it composed.
//
// Sanitising is the tempting answer and it is the wrong one. The standard
// library will do it silently for you — mime.QEncoding.Encode turns a CRLF into
// "=0D=0A", mime.FormatMediaType percent-escapes it — and in both cases the
// message that goes out is not the message the caller wrote, while the caller
// is told everything succeeded. This domain refuses instead, with a typed error
// that names the FIELD and never the value.
//
// The guards that enforce it — Validate and the per-field checks it runs —
// live in internal/service/app/mail with the composition of the MIME body and
// the SMTP transport that delivers it: checking a header, an address or an
// attachment against its grammar is a mechanism (ADR 0160). This package holds
// the values, the port, the header names and the refusals. Nothing here
// writes a byte to a socket.
//
// Code range: 0.2.31.* (ADR 0064).
//
// Package mail — the message as a value, and the envelope derived from it.
//
// Package mail — the transport port and its ADR 0039 capability siblings.
//
// They live apart from the value types because a port and a value change for
// different reasons: the port is FROZEN and the values are not.
//
// Package mail — one record of what a transport put on the wire.
package mail
