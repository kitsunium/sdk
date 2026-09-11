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
// Composition of the MIME body, and the SMTP transport that delivers it, live
// in internal/service/mail. Nothing here writes a byte to a socket.
//
// Code range: 0.2.31.* (ADR 0064).
package mail

// The octet boundaries the RFC 5322 grammars are expressed in. They are named
// because a bare 0x21 in a comparison is a number a reader has to look up, and
// because the same four bounds are used by three different guards.
const (
	// minPrintableASCII is "!", the first character RFC 5322 §3.6.8 admits in a
	// field name.
	minPrintableASCII byte = 0x21
	// maxPrintableASCII is "~", the last one.
	maxPrintableASCII byte = 0x7E
	// spaceOctet is the space, which ends an atom rather than belonging to one.
	spaceOctet byte = 0x20
	// delOctet is DEL, the first octet above the printable range.
	delOctet byte = 0x7F
)
