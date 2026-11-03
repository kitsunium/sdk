<!-- updated: 2026-11-03T00:00:00Z -->
# internal/core/data/codec/jsonschema/

## Purpose

The error codes and the sentinels of `internal/service/data/codec/jsonschema` — the JSON Schema 2020-12 validator (ADR 0210, #882) — and nothing else (ADR 0160 §2). The mechanism stays in the service package, which imports this one as `corejsonschema`.

Stdlib-only: this package imports `internal/kernel/errs` and nothing else.

## Surface

| File | Declares |
|---|---|
| `gen_codes.go` | 6 `errs.Code` constants, range `0.3.103.*`; 6 `errs.Define` sentinels, one per code — written by kit gen from `design/data/codec/jsonschema.yaml` (ADR 0164) |

## Error codes (range `0.3.103.*`)

| Code | Const | Sentinel | Reason |
|---|---|---|---|
| `0.3.103.1` | `CodeJSONSchemaSchemaInvalid` | `SchemaInvalid` | `SCHEMA_INVALID` — the schema does not compile |
| `0.3.103.2` | `CodeJSONSchemaKeywordRefused` | `KeywordRefused` | `KEYWORD_REFUSED` — a keyword out of scope, or another draft |
| `0.3.103.3` | `CodeJSONSchemaRefUnresolved` | `RefUnresolved` | `REF_UNRESOLVED` — a `$ref` naming nothing compiled or registered |
| `0.3.103.4` | `CodeJSONSchemaFormatUnknown` | `FormatUnknown` | `FORMAT_UNKNOWN` — a format to assert without a checker |
| `0.3.103.5` | `CodeJSONSchemaViolation` | `Violation` | `VIOLATION` — the code a violation carries when validation's codes do not name its keyword; 422 |
| `0.3.103.6` | `CodeJSONSchemaReferenceLoop` | `ReferenceLoop` | `REFERENCE_LOOP` — a `$ref` cycle consuming nothing |

No Public and no Private carries a byte of a schema or a document; a field names a keyword, a reference or a format, which the schema's author wrote.

## Do NOT

- Change a value: a renumbered code breaks every client that branches on it.
- Add compilation or validation here (ADR 0160 §4).
