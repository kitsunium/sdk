<!-- updated: 2026-09-28T16:42:12Z -->
# pkg/v1/app/mail/

## Purpose

The public facade for the mail domain (ADR 0064): type aliases over
`internal/core/app/mail`, the sentinels from both layers, and the constructors —
`NewSMTP`, `NewMemory`, `NewCapture`, `NewComposer` — plus `Compose`,
`Validate` and `EnvelopeOf` for a caller that wants the bytes, the verdict or
the envelope without a transport. And the durable outbox (ADR 0111): `NewSpool` over
`internal/service/app/mail/spool`, whose `Spool.SendWithID` queues a mail under an
identifier its caller minted (ADR 0141).

Consumer-facing prose lives in the package doc comment in `mail.go` and is
rendered into `README.md` by `gomarkdoc` (rule 10 / ADR 0008). This file is the
maintainer's half.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Message`, `Address`, `Attachment`, `HeaderField`, `Envelope`, `Delivery` | aliases | the values |
| `Transport` | alias | FROZEN at one method (ADR 0039) |
| `BatchSender`, `Outbox` | aliases | the capability siblings, reached by type assertion |
| `FullTransport` | alias | the union `NewMemory` returns |
| `SMTPConfig`, `TLSMode`, `ComposerConfig`, `Composer` | aliases | service-layer types |
| `TLSUnset`/`TLSStartTLS`/`TLSImplicit`/`TLSDisabled` | constants | the zero is refused |
| 19 sentinels | vars | all aliases of `core/app/mail`'s (ADR 0160): the 9 message refusals and the 10 outcomes of composition and the SMTP session — `InvalidURL` is `ParseURL`'s — plus the spool's six below |
| `NewSMTP`, `NewMemory`, `NewComposer`, `Compose`, `Validate`, `EnvelopeOf`, `ParseURL` | funcs | `ParseURL` reads `smtp://…?tls=…` / `smtps://…` into a config `NewSMTP` accepts, and never quotes the URL in a refusal |
| `NewCapture(keep)` / `DefaultCaptureKeep` | func / const | `NewMemory`'s double keeping the last `keep` deliveries (200 when not positive) — the development server's transport |
| `NewSpool`, `SpoolAttemptFrom` | funcs | the durable outbox and the attempt a delivery context carries (ADR 0111) |
| `Spool`, `SpoolConfig`, `SpoolEvent`, `SpoolEventKind`, `SpoolAttempt`, `SpoolDeadLetter` | aliases | onto `service/app/mail/spool` — `Spool`, `Config`, `EventValue`, `EventKind`, `AttemptValue`, `DeadLetterValue`; `Spool.SendWithID` reaches consumers through the alias, so the facade adds no function for it (ADR 0141) |
| `SpoolQueued` … `SpoolDuplicate` | constants | the five event kinds |
| `DefaultSpoolSendTimeout`, `DefaultSpoolRetryBase`, `DefaultSpoolRetryMax`, `DefaultSpoolMaxMessageBytes`, `SpoolDeliveredMemory` | constants | the spool's clamps |
| `SpoolMaxIDBytes` | constant | the bound on a spooled mail's identifier, whoever minted it (ADR 0141) |
| `SpoolMisconfigured`, `SpoolClosed`, `SpooledMailUndecodable`, `SpooledMailUnencodable`, `TransportPanicked`, `InvalidMailID` | vars | the spool's sentinels (`0.3.81.*`); `InvalidMailID` is `SendWithID`'s refusal of an identifier no mail can keep |

Error CODE constants are deliberately not re-exported. `errors.Is(err,
mail.HeaderInjection)` is the consumer-facing way to match one refusal —
the SDK error's `Is` method compares `(Code, Reason)` rather than pointers —
and `errs.CodeOf` covers the rest. This mirrors `pkg/v1/data/vfs`.

## Why this shape

- **Aliases, not wrappers.** A wrapper type would make a consumer's own
  `Transport` implementation fail to satisfy the SDK's, and would double every
  doc comment.
- **`NewMemory` takes no arguments**, like `vfs.NewMem`. Every knob it could
  offer is one a consumer's test has to set before it can assert anything, and
  the value of a double is that it costs one line.
- **`Compose` and `Validate` are exported free functions.** A caller who hands
  the bytes to a provider API this SDK does not implement still gets the guards;
  a caller validating a submitted form gets the same verdict at the edge that
  the transport would give at send time.
- **`EnvelopeOf` is a function, not a method of `Message`.** Deriving the
  envelope validates first, and the guards are the service's since ADR 0160 —
  a method of the core value could not reach them. It replaced
  `Message.Envelope()` while the module is v0 (ADR 0040), so a caller who used
  the method fails to compile rather than receiving an unvalidated envelope.
- **No `Send(ctx, transport, msg)` helper.** It would only hide which transport
  carried the message, for no line saved.

## Do NOT

- **Do NOT add a wrapper type or a second `Transport`-shaped interface.**
- **Do NOT re-export a mutable default transport.** ADR 0030's reasoning applies
  one step further: a package-level transport armed from an import would dial a
  network from an `init`.
- **Do NOT hand-edit `README.md`.** Edit the package doc comment in `mail.go`
  and run `go generate ./v1/mail/` (rule 10).

## Verification

```bash
cd pkg && GOWORK=off go test -race ./v1/mail/...
cd pkg && go generate ./v1/mail/   # regenerates README.md from the doc comment
```
