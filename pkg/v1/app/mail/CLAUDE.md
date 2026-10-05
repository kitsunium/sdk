<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/app/mail/

## Purpose

The public facade for the mail domain (ADR 0064): type aliases over
`internal/core/app/mail`, its sentinels (every one declared in the core,
ADR 0160), and the constructors —
`NewSMTP`, `NewMemory`, `NewCapture`, `NewComposer` — plus `Compose`,
`Validate` and `EnvelopeOf` for a caller that wants the bytes, the verdict or
the envelope without a transport. The durable outbox (ADR 0111, ADR 0141) is NOT here: it is
`pkg/v1/app/mail/spool`, so a program that only composes and sends links no
queue, no filesystem engine and no SQL port — `go list -deps` of this package
names none of `data/queue`, `data/vfs`, `data/sql` or `app/id`.

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
| 19 sentinels | vars | all aliases of `core/app/mail`'s (ADR 0160): the 9 message refusals and the 10 outcomes of composition and the SMTP session — `InvalidURL` is `ParseURL`'s |
| `NewSMTP`, `NewMemory`, `NewComposer`, `Compose`, `Validate`, `EnvelopeOf`, `ParseURL` | funcs | `ParseURL` reads `smtp://…?tls=…` / `smtps://…` into a config `NewSMTP` accepts, and never quotes the URL in a refusal |
| `NewCapture(keep)` / `DefaultCaptureKeep` | func / const | `NewMemory`'s double keeping the last `keep` deliveries (200 when not positive) — the development server's transport |

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
- **The spool is a package of its own.** It stands on the queue domain, and
  a durable spool on the filesystem and SQL ports beneath it. With it, this
  facade linked 23 SDK packages; without it, 7 (`go list -deps`, measured
  when it moved out) — the queue, `vfs` and `sql` engines, `id` and five
  kernel primitives are linked only by a program that imports
  `pkg/v1/app/mail/spool`. A child package is not linked by its parent's
  importers, so the split costs a caller nothing (ADR 0155).
- **No `Send(ctx, transport, msg)` helper.** It would only hide which transport
  carried the message, for no line saved.

## Do NOT

- **Do NOT add a wrapper type or a second `Transport`-shaped interface.**
- **Do NOT re-export a mutable default transport.** ADR 0030's reasoning applies
  one step further: a package-level transport armed from an import would dial a
  network from an `init`.
- **Do NOT hand-edit `README.md`.** Edit the package doc comment in `mail.go`
  and run `go generate ./v1/app/mail/` (rule 10).

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/app/mail.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the package comment and the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```bash
cd pkg && GOWORK=off go test -race ./v1/app/mail/...
cd pkg && go generate ./v1/app/mail/   # regenerates README.md from the doc comment
cd pkg && go list -deps ./v1/app/mail | grep -E 'data/(queue|vfs|sql)'   # prints nothing
```
