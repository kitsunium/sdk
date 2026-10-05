# pkg/v1/security/redact/

## Purpose

Public facade over `internal/core/security/redact` — the port, its values and
its codes — and `internal/service/security/redact` — the engine and its
configuration (ADR 0101, ADR 0160): render a Go value, a JSON document, a text
or log attributes for display with their secrets replaced, within an exact byte
bound, never touching the input.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `New(cfg)` | func | a `Redactor` applying `cfg` — the SDK's engine, returned as the port |
| `DefaultWords()` | func | the ten default name fragments, as a fresh slice |
| `Config` | type alias | `Words`, `Tag` (default `redact`), `Field`, `Error` |
| `Redactor` | type alias | `= coreredact.Redactor`, the port — `Name`, `Text`, `JSON`, `Value`, `Attrs`, frozen at five (ADR 0039) |
| `Document` | type alias | `= coreredact.DocumentValue` — `JSON` (never over the bound, always well-formed) + `Truncated` |
| `Placeholder`, `Ellipsis`, `MinBytes`, `Unencodable` | const | |
| `CodeDocumentInvalid`, `CodeValueUnencodable` | const | `0.3.73.*` |
| `DocumentInvalid`, `ValueUnencodable` | var | sentinels |

Every alias points at the layer that owns it (ADR 0074): `Redactor`,
`Document`, the four constants, the two codes and the two sentinels at the
core; `Config` and `DefaultWords` — one engine's construction parameters — at
the service.

**Shape change (v0, ADR 0040).** Until ADR 0160, `Redactor` aliased the
engine's struct and `New` returned `*Redactor`. It now aliases the port and
`New` returns `Redactor`: code that wrote the type as `*redact.Redactor`
writes `redact.Redactor`; code that let the compiler infer it compiles
unchanged, and every method call is the same call.

## Why-this-shape

- **The tag is configurable** so a framework keeps its own (`kit:"secret"`)
  while the SDK's spelling is `redact:"secret"`. `Config.Field` covers the
  rules a framework has that are not a tag option — a field bound to a cookie,
  to a header named like a secret.
- **`Attrs` takes `[]logger.Attr`** — the facade's `logger.Attr` is the same
  type — and returns an iterator, so a log panel bounds the count by breaking.
- **The package doc says what is NOT recognised**, because a display filter that
  let a reader believe it recognised everything would be worse than none.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`redact.go` (ADR 0008). Regenerate with `make docs-readme`; do not hand-edit it.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/security/redact.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the package comment and the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```sh
bazel test --config=race //pkg/v1/security/redact:redact_test
cd pkg && GOWORK=off go test -race ./v1/security/redact/
```
