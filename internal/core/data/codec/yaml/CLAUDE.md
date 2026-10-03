<!-- updated: 2026-10-03T00:00:00Z -->
# internal/core/data/codec/yaml/

## Purpose

The error codes and the sentinels of `internal/service/data/codec/yaml` — the YAML codec (a named subset of YAML 1.2.2) — and nothing else. They were declared in that service package until ADR 0160 §2 put every code in the core, at the path that mirrors the package emitting it; they moved with their values, reasons, public and private texts unchanged, so no rendering, no `errs.HasCode` match and no log query changed. The mechanism — encoding, decoding, the bounds, and the constructors that wrap a failure's detail around these codes — stays in the service package, which imports this one as `coreyaml`.

Stdlib-only: this package imports `internal/kernel/errs` and nothing else.

## Surface

| File | Declares |
|---|---|
| `codes.go` | 11 `errs.Code` constants, range `0.3.4.*` |
| `errors.go` | 11 `errs.Define` sentinels, one per code |

## Error codes (range `0.3.4.*`)

| Code | Const | Sentinel | Reason |
|---|---|---|---|
| `0.3.4.1` | `CodeYAMLMarshalFailed` | `MarshalFailed` | `MARSHAL_FAILED` |
| `0.3.4.2` | `CodeYAMLUnmarshalFailed` | `UnmarshalFailed` | `UNMARSHAL_FAILED` |
| `0.3.4.3` | `CodeYAMLAnchorRefused` | `AnchorRefused` | `ANCHOR_REFUSED` |
| `0.3.4.4` | `CodeYAMLAliasRefused` | `AliasRefused` | `ALIAS_REFUSED` |
| `0.3.4.5` | `CodeYAMLTagRefused` | `TagRefused` | `TAG_REFUSED` |
| `0.3.4.6` | `CodeYAMLMergeKeyRefused` | `MergeKeyRefused` | `MERGE_KEY_REFUSED` |
| `0.3.4.7` | `CodeYAMLMultiDocRefused` | `MultipleDocumentsRefused` | `MULTIPLE_DOCUMENTS_REFUSED` |
| `0.3.4.8` | `CodeYAMLComplexKeyRefused` | `ComplexKeyRefused` | `COMPLEX_KEY_REFUSED` |
| `0.3.4.9` | `CodeYAMLDirectiveRefused` | `DirectiveRefused` | `DIRECTIVE_REFUSED` |
| `0.3.4.10` | `CodeYAMLDuplicateKey` | `DuplicateKey` | `DUPLICATE_KEY` |
| `0.3.4.11` | `CodeYAMLLeadingZeroRefused` | `LeadingZeroRefused` | `LEADING_ZERO_REFUSED` |

Every refusal by name (`0.3.4.3`–`0.3.4.11`) is wrapped by the codec with `CodeYAMLUnmarshalFailed` in its trail, so `errs.HasCode(err, CodeYAMLUnmarshalFailed)` answers for all of them; that wrapping is the service's (`refused`, in `failed.go`).

A `pkg/v1` facade that names these aliases this package, never the service one (ADR 0074).

## Do NOT

- Change a value to make it fit this directory. `LL = 3` records the layer that allocated the range, not the one that declares it (ADR 0160 §3); a renumbered code breaks every dashboard, alert rule and client that branches on it.
- Add encoding, decoding, a bound or a failure constructor here: that is mechanism, and it stays in `internal/service/data/codec/yaml` (ADR 0160 §4).
- Import `net/http` for a status: `errs.WithHTTPStatus` takes an int.
- Declare a code outside `0.3.4.*`, the range `codeRangeOwners` gives this directory.

## Verification

```sh
bazel test --config=race //internal/core/data/codec/yaml:yaml_test
cd internal/core && GOWORK=off go test -race ./data/codec/yaml/
```

`TestCodesKeepTheirValues` pins every code to its literal value and to the sentinel and reason bound to it. The range-ownership audit (`bazel test //internal/kernel/errs:errs_test`) judges this package against `codeRangeOwners`, through `//:audit_sources`.
