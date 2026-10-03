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
package mail
