// Package mail — range 0.3.61.* (ADR 0064 service/mail block).
package mail

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.61.0 - 0.3.61.255

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
