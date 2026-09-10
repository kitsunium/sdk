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
