// Package mail — one record of what a transport put on the wire.
package mail

// DeliveryValue is one record of what a transport actually put on the wire.
//
// It carries the composed BYTES and not the message, because those are two
// different assertions. A test that checks the message checks what it itself
// built a moment earlier; a test that checks Raw checks what the composer
// produced — the encoded subject, the multipart structure, the absence of a
// Bcc header — which is the only half that can be wrong.
type DeliveryValue struct {
	// Envelope is the MAIL FROM and the RCPT TO list the session would issue.
	Envelope EnvelopeValue
	// Raw is the RFC 5322 message: headers, blank line, MIME body, CRLF line
	// endings throughout.
	Raw []byte
}

// EnvelopeValue is what an SMTP session actually carries: one MAIL FROM and one
// or more RCPT TO (RFC 5321 §3.3). It is NOT the header block, and the
// difference is the whole reason this type exists.
//
// A receiving MTA routes on the envelope and never on the headers. To, Cc and
// Bcc are text a client renders; the envelope is the delivery instruction. They
// agree by convention and not by protocol, which is how a Bcc can exist at all
// — and how a forwarding rule can deliver a message to somebody who appears in
// no header.
type EnvelopeValue struct {
	// From is the return path (RFC 5321 §4.1.1.2): where a bounce goes. It is
	// the message's From addr-spec.
	From string
	// To is every RCPT TO the session will issue, in To, Cc, Bcc order. Bcc
	// recipients appear HERE and in no header.
	To []string
}
