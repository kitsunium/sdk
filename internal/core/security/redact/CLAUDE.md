<!-- updated: 2026-10-03T07:14:15Z -->
# internal/core/security/redact/

## Purpose

Declares the **display-redaction port** (ADR 0101; a core since ADR 0160):
the `Redactor` that renders a Go value, a JSON document, a text or log
attributes for DISPLAY with every secret it recognises replaced, within a byte
bound, never mutating its input — and the values every implementation shares
and every caller compares against. The engine — the three rules (a NAME, a
DECLARATION, a URL's CREDENTIALS), the writer that counts every byte before
writing it, the per-type plan cache — and its `Config` live in
`internal/service/security/redact`, whose `*Redactor` implements the port.

Stdlib (`encoding/json` for `json.RawMessage`, `iter`) plus
`internal/core/observe/logger` for the attribute shape `Attrs` takes, and
`internal/kernel/errs`.

## Surface

| File | Symbol | Notes |
|---|---|---|
| `redact.go` | `Redactor` | the port: `Name` / `Text` / `JSON` / `Value` / `Attrs` — **FROZEN at five** (ADR 0039), guarded by `TestRedactorIsFrozenAtFiveMethods` |
| `redact.go` | `Placeholder` | `"[redacted]"` — what every secret becomes |
| `redact.go` | `Ellipsis` | `"…"` — one character, three bytes, counted inside the bound |
| `redact.go` | `MinBytes` | `16` — the floor every bound is raised to (ADR 0031's clamp) |
| `redact.go` | `Unencodable` | `"[unencodable]"` — what `Attrs` shows for a value `encoding/json` refuses |
| `redact.go` | `DocumentValue` | `JSON` (one well-formed value, never over the bound) + `Truncated` — what `JSON` and `Value` return |
| `codes.go` | `CodeDocumentInvalid`, `CodeValueUnencodable` | `0.3.73.1`, `0.3.73.2` |
| `errors.go` | `DocumentInvalid`, `ValueUnencodable` | `errs.Define` |

## Error codes

Range `0.3.73.*`. It was allocated in the service layer (`LL = 3`) when the
engine was the whole domain (ADR 0101 §D5), and is declared here since
ADR 0160 gave the domain a core — `LL` records the layer that allocated a
range, not the directory its declaration lives in, so the values did not
change. `codeRangeOwners` names this directory for `0x00_03_49_00`.

| Code | Reason | When |
|---|---|---|
| `0.3.73.1` | `DOCUMENT_INVALID` | `JSON` got something that is not exactly one JSON value; nothing is returned for it |
| `0.3.73.2` | `VALUE_UNENCODABLE` | `Value` got something `encoding/json` refuses; the type is named, never the value |

Neither refusal repeats a byte of what it refused, and both are raised only by
the engine — their `Private` names `service/security/redact`, the package that
raises them.

## Why this shape

- **A port, because a consumer needs a double.** ADR 0101 §D5 shipped one
  engine and no contract; ADR 0160 gives every service domain a core, and the
  port is what lets code that accepts a `Redactor` be handed a test double.
  `pkg/v1/security/redact.Redactor` aliases it and `New` returns it.
- **The values are the contract's, the configuration is the engine's.** A
  second implementation must write the same `Placeholder` (a caller compares
  against it), end a cut in the same `Ellipsis`, honour the same `MinBytes`
  floor and return the same `DocumentValue`, so they are here. `Config` and
  `DefaultWords` describe how ONE engine recognises a secret, which a second
  implementation would not have to accept, so they stay with it (ADR 0074).
- **`Attrs` takes the logger's own `AttrValue`.** A log panel ranges over a
  record's attributes as the logger built them; a parallel attribute type would
  need a conversion per record. The core-to-core import is a lateral one
  (`internal/core/CLAUDE.md` §Imports allowed).

## Do NOT

- Add a method to `Redactor`. Widen by a sibling interface (ADR 0039).
- Put a rule, a pattern or the writer here. Recognising a secret and counting
  bytes is the engine's mechanism; the core says what the result is.
- Change `Placeholder`, `Ellipsis` or `MinBytes`. A consumer compares against
  the first, and a document cut at the floor must still hold the second.
- Renumber a code to match this directory. Nothing derives a code from a path,
  and a consumer branches on the value (ADR 0160).

## Verification

```sh
bazel test --config=race //internal/core/security/redact:redact_test
cd internal/core && GOWORK=off go test -race ./security/redact/
```
