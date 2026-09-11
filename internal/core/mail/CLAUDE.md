# internal/core/mail/

## Purpose

Declares the **electronic-mail port**: `Transport` (frozen at one method), its
ADR 0039 capability siblings `BatchSender` and `Outbox`, their union
`FullTransport`, the message as a VALUE (`MessageValue`, `AddressValue`,
`AttachmentValue`, `HeaderFieldValue`), the SMTP `EnvelopeValue` derived from it, and the guards every
implementation runs — `Validate`, `ValidateHeaderName`, `ValidateHeaderValue`,
`ValidateAddress`, `ValidateAttachment`. The 28th core sibling, admitted by
**ADR 0064**.

MIME composition and the SMTP session live in `internal/service/mail`. Nothing
here writes a byte to a socket.

Code range: `0.2.31.*` (ADR 0064).

## Contents

One exported struct per file, named after it — the layer's convention, the same
one `core/metrics` follows with `AttrValue` / `ScopeValue` / `SnapshotValue`.
`pkg/v1/mail` aliases them back to the short names a consumer writes
(`mail.Message`, `mail.Address`), exactly as `pkg/v1/metrics.Attr` does.

| File | What lives there |
|---|---|
| `mail.go` | the package doc and the four octet bounds the grammars are written in |
| `message_value.go` | `MessageValue` and `MessageValue.Envelope` — where Bcc becomes RCPT TO and nothing else |
| `address_value.go` | `AddressValue` and `IsZero` |
| `attachment_value.go` | `AttachmentValue`, `Inline`, `DefaultAttachmentType` |
| `header_field_value.go` | `HeaderFieldValue` |
| `envelope_value.go` | `EnvelopeValue` |
| `delivery_value.go` | `DeliveryValue` |
| `transport_interface.go` | `Transport`, `BatchSender`, `Outbox`, `FullTransport` |
| `header.go` | the field-name constants, the reserved set, and the INJECTION GATE |
| `address.go` | `ValidateAddress`, `NeedsQuotedDisplayName`, the dot-atom grammar |
| `attachment.go` | `ValidateAttachment` and its three sub-guards |
| `validate.go` | `Validate` — the one whole-message guard both transports run |
| `codes.go` | the nine `Code` constants, `0.2.31.1` … `0.2.31.9` |
| `errors.go` | the nine sentinels, each var named for its `Define` reason |

## The injection gate is the domain

RFC 5322 §2.2 ends a header field at CRLF. So a CR or an LF inside a value does
not *corrupt* the field — it TERMINATES it, and every octet after it becomes a
new header line. `"Hi\r\nBcc: attacker@example.com"` is a well-formed subject
followed by a well-formed Bcc, and the message is delivered to a party the
sender cannot see in what it composed.

`ValidateHeaderValue` **refuses**. It does not sanitise, and the standard
library is the argument:

| stdlib call | handed `"a\r\nBcc: x@y"` | verdict |
|---|---|---|
| `mime.QEncoding.Encode` | `"=?utf-8?q?a=0D=0ABcc:_x@y?="` | repairs, silently |
| `mime.FormatMediaType` | `filename*=utf-8''a%0D%0ABcc%3A…` | repairs, silently |
| `net/mail.Address.String()` (Name) | RFC 2047 encoded | repairs, silently |
| `net/mail.Address.String()` (**Address**) | `"ok" <u@exa\r\nmple.com>` | **passes it through** |
| `mime/multipart.Writer.CreatePart` | header emitted verbatim | **passes it through** |

Three of those produce a deliverable message that is not the one the caller
wrote while reporting success; two produce the injection itself. That is why
this package validates rather than delegates, and why `AddressValue` is not
`net/mail.Address`.

A bare CR and a bare LF are refused as well as the pair, because receivers
disagree about a lone LF and some normalise it to CRLF — reconstructing the
attack downstream from an input that looked survivable here. NUL joins them.

## Bcc has exactly one treatment

RFC 5322 §3.6.3 permits three. This domain takes the only one that cannot leak:
the addresses reach `Envelope.To` and become RCPT TO commands, and no header
names them. The other two — one message per blind recipient carrying only their
own Bcc line, or the header intact to everybody — are respectively N messages
the caller did not ask for and a disclosure RFC 5321 §7.2 warns about by name.

`Bcc` is also in `reservedHeaders`, so a caller cannot spell it by hand either.
That is the same attack arriving through a supported API.

## Refusals, never defaults

- **No sender, no recipients, no body** → refused (`MissingSender`,
  `NoRecipients`, `EmptyBody`). A zero message is an unfilled struct, not a
  decision to send nothing (ADR 0031).
- **An inline attachment in a message with no body** → refused. A `cid:`
  reference has nothing to be referenced from, and `multipart/related` needs a
  root part (RFC 2387 §3.1). Demoting it to a regular attachment would silently
  turn an embedded image into a file to download.
- **A non-ASCII addr-spec and a quoted local part** → refused BY NAME
  (`UnsupportedAddress`), not reported as malformed. Both are legal mail; the
  alternatives to refusing are punycoding a domain or dropping accents from a
  local part, and both deliver — to somebody else.
- **`Date`** is the one clamp: RFC 5322 §3.6.1 makes it required and "now" is
  the only value a zero could have meant. The composer stamps it from its clock.
- **`Message-ID`** is neither: absent means no header, because RFC 6409 §8.2
  makes adding one the submission server's job and generating one here would
  mean inventing both entropy and a domain the SDK does not own.

## Conventions

- **No `Public` string here carries a value.** Not a header value, not an
  address, not a subject, not an attachment's bytes. A `Public` travels to
  strangers and the values this domain refuses are, by construction, the ones an
  attacker chose. The field NAME travels where it helps; the value travels only
  as a log-only field, reachable through `errs.FieldsOf`.
  `TestHeaderInjectionErrorNeverDisclosesTheValue` is the guard.
- **`Validate` stops at the first problem.** That is the opposite of ADR 0046's
  collect-all and it is deliberate: a validation report describes a form a human
  fixes field by field, while this is a security gate, and a gate that keeps
  evaluating a message it has already refused is doing work an attacker chose.
- **The guards are shared, not reimplemented.** Both transports call `Validate`,
  which is what makes the in-memory one a faithful double.
- **`Transport` is frozen at one method.** `TestTransportStaysFrozenAtOneMethod`
  and `TestCapabilitiesAreSiblingsAndNotMembers` are the ADR 0039 guards.
- **A value type carries the `Value` suffix.** It is the layer's convention and
  it is what keeps the consumer-facing names short: `pkg/v1/mail.Message` is a
  type ALIAS of `MessageValue`, so a caller never types the suffix and a
  maintainer never wonders whether a bare `Message` is a port or a value.

## Do NOT

- **Do NOT add a second method to `Transport`.** New capability → new sibling
  interface, reached by type assertion. See ADR 0039.
- **Do NOT sanitise a header instead of refusing it.** A message that goes out
  altered, with the caller told everything succeeded, is worse than one that
  does not go out — the caller cannot fix input it never learns about.
- **Do NOT emit a `Bcc` header, ever**, and do not remove it from
  `reservedHeaders`.
- **Do NOT put a header value, an address or a subject in a `Public`.** Field,
  not `Public`.
- **Do NOT reach for `net/mail.Address` for formatting.** It emits its `Address`
  field verbatim — see the table above.

## Verification

```bash
cd internal/core && GOWORK=off go test -race ./mail/...
```
