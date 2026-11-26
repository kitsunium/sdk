# internal/service/app/mail/ — reference

Moved unchanged from `internal/service/app/mail/CLAUDE.md` (ADR 0253); each section keeps its title.

## The SMTP transport, and what it commits to

It is built ON `net/smtp` and does not reimplement RFC 5321. That package is
**frozen** upstream — its own doc says "The smtp package is frozen and is not
accepting new features" — which is a statement about features, not maintenance:
it still receives security fixes, and SMTP has not gained a feature this domain
wants since RFC 3207.

What this package commits to maintaining is the POLICY above it, which is where
every decision that matters lives:

| Decision | Why `net/smtp` does not make it |
|---|---|
| A required STARTTLS is REFUSED when unadvertised | the stdlib has no policy; the caller writes the loop |
| Credentials never travel unencrypted | `smtp.PlainAuth` sends them in the clear to a server named `localhost` — `if !server.TLS && !isLocalhost(server.Name)` |
| `ServerName` is filled from `Host` | `Client.StartTLS` passes the `*tls.Config` to `tls.Client` unchanged, so an empty name fails the handshake and the obvious escape is `InsecureSkipVerify` |
| A context stops a send | `net/smtp` takes no context; the transport severs the socket the client is blocked in |

If `net/smtp` is ever deprecated rather than frozen, the client is replaced in
`smtp.go` and `session.go`, and `coremail.Transport` does not change — which is why the port has
one method and no session on it.

**There is no opportunistic STARTTLS mode.** Encrypt-if-offered, continue-in-the-clear-if-not
is what most mail libraries ship, and it means one stripped EHLO line hands an
attacker the whole session while the sender's logs show success. It cannot be
configured here because it is not implemented.

**`Send` and `SendBatch` share one composition path** (`smtpTransport.prepare`),
so a message that would be refused is refused identically whichever call site it
arrives through — and before a socket exists either way.

**A refused MAIL, RCPT or DATA keeps its reply reachable** (`refused`,
ADR 0111 amended 2026-10-21): `SEND_REFUSED` is the origin and the server's
`*textproto.Error` its source, so `errors.As` reads the reply code — a 5xx is
permanent and the mail spool dead-letters it at once, a 4xx is retried. The
reply's text is still only a log-only field. A 5xx to the greeting, EHLO,
STARTTLS or AUTH is NOT kept reachable: it is about the relay or this
program's configuration, never the mail, and a spool that dead-lettered every
mail on a wrong password would lose them all.

**No connection pool.** An SMTP session is stateful, a pooled one must be
revalidated with a NOOP before every reuse, and the caller who wants one session
for many messages already has `BatchSender`.

## The memory transport is a double, not a stub

`NewMemory` **composes**. A stub that recorded the `MessageValue` would accept
a subject carrying a CRLF, an inline attachment with no body and a Bcc the
caller expected in a header — every refusal the SMTP transport makes, made
nowhere — and a consumer's suite would go green right up to production.

`DeliveryValue` carries the composed BYTES rather than the message, because those are
different assertions: checking the `Message` checks what the test itself built a
moment earlier, while checking `Raw` checks the encoded subject, the multipart
structure and the absence of a Bcc header.
