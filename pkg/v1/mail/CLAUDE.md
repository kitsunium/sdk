# pkg/v1/mail/

## Purpose

The public facade for the mail domain (ADR 0064): type aliases over
`internal/core/mail`, the sentinels from both layers, and three constructors —
`NewSMTP`, `NewMemory`, `NewComposer` — plus `Compose` and `Validate` for a
caller that wants the bytes or the verdict without a transport.

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
| 15 sentinels | vars | 9 from core, 6 from service |
| `NewSMTP`, `NewMemory`, `NewComposer`, `Compose`, `Validate` | funcs | |

Error CODE constants are deliberately not re-exported. `errors.Is(err,
mail.HeaderInjection)` is the consumer-facing way to match one refusal —
`errs.Is` compares `(Code, Reason)` rather than pointers — and `errs.CodeOf`
covers the rest. This mirrors `pkg/v1/vfs`.

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
