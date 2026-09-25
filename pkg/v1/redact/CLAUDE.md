# pkg/v1/redact/

## Purpose

Public facade over `internal/service/redact` (ADR 0101): render a Go value, a
JSON document, a text or log attributes for display with their secrets replaced,
within an exact byte bound, never touching the input.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `New(cfg)` | func | a `*Redactor` applying `cfg` |
| `DefaultWords()` | func | the ten default name fragments, as a fresh slice |
| `Config` | type alias | `Words`, `Tag` (default `redact`), `Field`, `Error` |
| `Redactor` | type alias | `Name`, `Text`, `JSON`, `Value`, `Attrs` |
| `Document` | type alias | `= svcredact.DocumentValue` — `JSON` (never over the bound, always well-formed) + `Truncated` |
| `Placeholder`, `Ellipsis`, `MinBytes`, `Unencodable` | const | |
| `CodeDocumentInvalid`, `CodeValueUnencodable` | const | `0.3.73.*` |
| `DocumentInvalid`, `ValueUnencodable` | var | sentinels |

All types are aliases onto the service: there is no port, and the values are
the engine's (ADR 0074).

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

## Verification

```sh
bazel test --config=race //pkg/v1/redact:redact_test
cd pkg && GOWORK=off go test -race ./v1/redact/
```
