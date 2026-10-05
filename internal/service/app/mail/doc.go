// Package mail — the addr-spec subset this domain accepts, and the display-name
// rule the composer depends on.
//
// Package mail — the attachment guards.
//
// Package mail — the composition helpers: media types, part construction, and
// the size estimate that keeps the output buffer to one allocation.
//
// Package mail — the composer: a message value in, RFC 5322 wire bytes out.
//
// Package mail — the composer's optional wiring.
//
// Package mail — the per-message build state, and the MIME structure it
// assembles.
//
// Package mail — the MIME entity tree and the writer that renders it.
//
// The renderer emits the multipart delimiters itself rather than using
// mime/multipart.Writer, and the reason is one measured line of that package:
// Writer.CreatePart formats header values VERBATIM, so a Content-Disposition
// carrying "a\r\nBcc: x@y" is emitted with the injected header intact. Every
// header written here goes through writeHeader, which runs the injection gate
// at the point of writing.
//
// The parser is the standard library's and always will be — mime/multipart's
// Reader is what the tests reparse composed messages with, because a test that
// decodes with the same code that encoded preserves exactly the bug it exists
// to catch.
//
// Package mail — the SMTP envelope, derived from a message after validating it.
//
// Package mail — the one helper through which this package raises a sentinel
// over a cause. The domain's codes and sentinels — the message refusals, and
// the outcomes of composition and of the SMTP session — are declared in
// internal/core/app/mail (ADR 0160).
//
// Package mail — the header grammar, and the injection gate every writer in
// this domain runs before a byte reaches a buffer.
//
// Package mail — RFC 2047 encoding and RFC 5322 folding for header lines.
//
// Package mail — the fixed-width line breaker every base64 body streams
// through.
//
// Package mail — the in-memory transport, which is a DOUBLE and not a stub.
//
// Package mail — one open SMTP conversation.
//
// Package mail — the SMTP transport.
//
// It is built ON net/smtp and does not reimplement RFC 5321. The package is
// FROZEN upstream — its own documentation says "The smtp package is frozen and
// is not accepting new features" — which is a statement about features and not
// about maintenance: it still receives security fixes, and the protocol it
// implements has not gained a feature this domain wants since RFC 3207.
//
// What this package therefore commits to maintaining is the POLICY above it,
// which is where every decision that matters lives: that a required STARTTLS
// is refused rather than downgraded, that credentials never travel over an
// unencrypted session, that the verified TLS name is set (net/smtp does not set
// it), and that a context actually stops a send (net/smtp has no context at
// all). If net/smtp is ever deprecated rather than frozen, the client is
// replaced here and [coremail.Transport] does not change — which is the reason
// the port has one method and no session on it.
//
// Package mail — compile-time port conformance for the two transports.
//
// These assertions live apart from the implementations because a failure here
// is a statement about the PORT, not about a function.
//
// Package mail — the SMTP transport's configuration, and its zero value.
//
// Package mail — how an SMTPConfig renders: every rendering the password could
// leak through writes the placeholder instead.
//
// Package mail — reading an SMTP URL into an SMTPConfig.
//
// Package mail — the one whole-message guard both transports run.
package mail
