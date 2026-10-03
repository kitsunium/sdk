<!-- updated: 2026-09-28T23:59:00Z -->
# pkg/v1/data/codec/jsonpatch/

## Purpose

Public facade for the structural difference between two JSON documents as
RFC 6902 operations with both values (ADR 0143 §D9). Aliases onto
`internal/service/data/codec/jsonpatch` plus the forwarding `Diff`, and onto
`internal/core/data/codec/jsonpatch` for `CodeNotJSON` and `NotJSON`, which
are declared there (ADR 0160). No logic lives here.

## Surface

| Symbol | Role |
|---|---|
| `Diff(from, to)` | the operations that turn `from` into `to`, in the order they apply; an empty slice for the same value |
| `Edit` | `Op`, `Path` (an RFC 6901 pointer), `Value` (written: add, replace), `Old` (replaced or removed: replace, remove); encodes as an RFC 6902 operation, `Old` under `old` |
| `Op` | `Add`, `Remove`, `Replace` |
| `CodeNotJSON`, `NotJSON` | `0.3.90.1`: a document that is not exactly one JSON value, named with the offset, never quoted |

## Why a package of its own, under codec

The comparison is of JSON documents, so it lives in the codec tree beside
`strictjson` and `jsonshape`; it has its own facade for their reason:
`pkg/v1/data/codec` blank-imports every codec, and a diff links none of them —
nor `net/http`, since `NotJSON`'s 400 became a literal in the core.

## Do NOT

- Add logic here; it belongs in `internal/service/data/codec/jsonpatch`.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Verification

```
bazel test --config=race //pkg/v1/data/codec/jsonpatch:jsonpatch_test
cd pkg && GOWORK=off go test -race ./v1/data/codec/jsonpatch/
```

## Reference

- ADR 0143 §D9; `internal/service/data/codec/jsonpatch/CLAUDE.md`
