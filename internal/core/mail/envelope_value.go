// Package mail — the SMTP envelope, and the reason Bcc has no header.
package mail

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
