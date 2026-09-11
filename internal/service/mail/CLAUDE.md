# internal/service/mail/

## Purpose

The two halves the `mail` port needs: **MIME composition** (a `Message` value
in, the RFC 5322 wire form out) and **two transports** — SMTP over `net/smtp`, and an
in-memory double. ADR 0064.

Code range: `0.3.61.*`.

## Contents

| File | What lives there |
|---|---|
| `composer.go` | `Composer`, `Compose`, and the message header block |
| `composer_config.go` | `ComposerConfig` — the clock and the randomness source, both clamping |
| `composition.go` | the per-message build state, the boundary generator, and the structure table |
| `compose.go` | the media-type helpers, the part builders, and `estimateSize` |
| `entity.go` | the MIME entity tree and the writer that renders it — including the multipart delimiters, written here rather than by `mime/multipart` |
| `line_wrapper.go` | the streaming 76-octet line breaker every base64 body goes through |
| `headerwrite.go` | RFC 2047 encoding, RFC 5322 folding, and the injection gate re-run at the point of writing |
| `smtp.go` | the SMTP transport: prepare, dial, negotiate, authenticate, send |
| `session.go` | one open SMTP conversation — the client and the policy that governs it |
| `smtpconfig.go` | `SMTPConfig`, `TLSMode`, and the refusals at construction |
| `smtp_compliance.go` | the compile-time port conformance assertions |
| `memory.go` | the in-memory transport — a DOUBLE, because it composes |
| `codes.go` / `errors.go` | the nine `0.3.61.*` codes, their sentinels, and `wrapAs` |
| `BENCH.md` | composition cost, cost per MiB of attachment, and the one measured optimisation |

## The MIME structure is a function of the populated fields

```
text only                    → text/plain
HTML only                    → text/html
text + HTML                  → multipart/alternative(plain, html)
body + inline parts          → multipart/related(body, inline…)
body + attachments           → multipart/mixed(body, attachment…)
body + inline + attachments  → multipart/mixed(multipart/related(body, inline…), attachment…)
```

Three details in that table are where implementations get it wrong:

- **Plain comes FIRST in the alternative.** RFC 2046 §5.1.4 orders alternatives
  "in increasing order of preference" and a receiver displays the last one it
  understands. HTML first asks every client to show the plain text.
- **`multipart/related` carries a `type` parameter**, which RFC 2387 §3.1 makes
  REQUIRED and which names the root part's media type. `start` is omitted
  because the root is first, which §3.2 already defaults to.
- **No container is emitted for a single child.** A `multipart/mixed` with one
  part makes some clients show a paperclip on a message with no attachment.

Every case is asserted by REPARSING the composed bytes with `net/mail` and
`mime/multipart` — never by comparing a golden string, which only says the
output did not change.

## We write the delimiters; the stdlib reads them

`mime/multipart.Writer.CreatePart` formats header values **verbatim**. Handed a
`Content-Disposition` carrying `"a\r\nBcc: x@y"` it emits exactly that, injected
header and all — measured in `TestMultipartWriterWouldHaveInjected`, which
asserts the standard library's behaviour and skips (rather than passing
silently) if a future Go release changes it.

That is a reasonable contract for a package whose callers control their own
headers, and a trap for one whose callers do not. So this package emits the
`--boundary` framing itself (twenty lines, RFC 2046 §5.1.1) and every header —
message-level and part-level — goes through `writeHeader`, which re-runs
`coremail.ValidateHeaderValue` at the point of writing.

`mime/multipart.Reader` is used and always will be: it is what the tests reparse
with, because a test that decodes with the code that encoded preserves exactly
the bug it exists to catch.

## Boundaries cannot collide, structurally

Every boundary is `=_<32 hex>_<n>`, and both halves of "cannot collide" are
proved rather than assumed:

- **A boundary cannot appear in a part body.** Every leaf is quoted-printable or
  base64. QP escapes `=` as `=3D` and reaches a literal `=` only as a soft line
  break at end of line; base64 reaches one only as trailing padding. So the two
  octets `=_` cannot occur in either. `TestBoundaryCannotAppearInAnyEncodedBody`
  feeds bodies that try.
- **Two boundaries in one message cannot match.** They share one random token
  and differ by a counter held on the per-message `composition`.
  `TestNestedBoundariesAreAlwaysDistinct` runs with a randomness source that
  returns a CONSTANT, so if distinctness depended on the draw it would fail
  every time.

## Encoding decisions

- **RFC 2047 only when necessary.** `mime.QEncoding.Encode` leaves a pure-ASCII
  value untouched, and that is the behaviour wanted: encoding "Invoice 4711"
  into `=?utf-8?q?Invoice_4711?=` makes every captured message and every log
  line unreadable to buy nothing, since a receiver renders both identically.
  Measured at 1.6× (BENCH.md).
- **Folding at existing whitespace only** (RFC 5322 §2.2.3). Unfolding removes
  the CRLF and keeps the WSP, so a fold at a space reconstructs the value
  exactly. A token with no whitespace and more than 998 octets has no legal fold
  point and is REFUSED (`HeaderTooLong`) rather than cut — cutting delivers a
  different value and reports success.
- **Text is quoted-printable, attachments are base64.** QP guarantees the
  §2.1.1 line limits and 7-bit safety without needing 8BITMIME, which no
  transport here negotiates. Base64 is wrapped at 76 octets (RFC 2045 §6.8) by a
  streaming `lineWrapper` rather than by materialising the encoded string.
- **`charset=utf-8` is always present on a text part.** Without it RFC 2045
  §5.2 defaults to us-ascii and every accent becomes a question mark on a
  receiver that believes the header over the bytes.
- **The filename is emitted twice**, as Content-Disposition `filename` and as
  the deprecated Content-Type `name` (RFC 2183 §2.3). Receivers in the field
  still read the second; both are rendered from one value, so they cannot
  disagree.

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

## Do NOT

- **Do NOT build parts with `mime/multipart.Writer`.** See above; `CreatePart`
  writes header values verbatim.
- **Do NOT add an opportunistic TLS mode**, and do not make `TLSUnset` mean
  anything but a refusal.
- **Do NOT call `client.Auth` before checking `client.TLSConnectionState()`.**
  `smtp.PlainAuth`'s own guard has a localhost carve-out.
- **Do NOT put a server reply, a host or a credential in a `Public`.** A reply
  is unbounded third-party text; it travels as a field.
- **Do NOT make the memory transport skip composition** to make a test faster.
  That is the one thing it is for.
- **Do NOT hand-tune `estimateSize` without re-running the profile.** It is a
  measured optimisation (BENCH.md); an estimate that is too small reintroduces
  the doubling it removed.

## Verification

```bash
cd internal/service && GOWORK=off go test -race ./mail/...
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./mail/
```

No test in this package touches the network. The SMTP cases run against
`fakesmtp_external_test.go`, a hand-written server on a local `net.Listener`
that speaks the subset the transport uses — and, crucially, lets a test DECIDE
what to advertise, which is where every interesting case lives. Its TLS
certificate is minted in-process with `crypto/ed25519`.
