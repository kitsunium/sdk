<!-- updated: 2026-09-26T00:00:00Z -->
# pkg/v1/codec/jsonshape/

## Purpose

Public facade for describing how values of a Go type look on the wire under
`encoding/json` (ADR 0133): kinds, members, which may be missing or null, and
the Go field behind each member. Aliases onto `internal/service/codec/jsonshape`
plus the forwarding `Of` and `For`. No logic lives here.

## Surface

| Symbol | Role |
|---|---|
| `Of(t)`, `For[T]()` | the description of a type; never fails, a new tree each call |
| `Shape` | `Kind`, `Name`, `Format`, `Nullable`, `Fields`, `Items`, `Values`, `Ref` |
| `Field` | `Name`, `Shape`, `Optional`, `Quoted`, `Rules` — and `GoName`, `Tag`, `Index` for the Go field |
| `Kind` | `Any` (the zero value), `Object`, `Array`, `Map`, `String`, `Integer`, `Number`, `Boolean`, `Unsupported`; encodes as its name |

## Why a package of its own, under codec

The description is `encoding/json`'s, so it lives in the codec tree — not in
`validation`, whose only link to it is the `validate` tag a member carries as
text. It has its own facade for `strictjson`'s reason: `pkg/v1/codec`
blank-imports every codec, and describing a type links none of them.

## Do NOT

- Add logic here; it belongs in `internal/service/codec/jsonshape`.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Verification

```
bazel test --config=race //pkg/v1/codec/jsonshape:jsonshape_test
cd pkg && GOWORK=off go test -race ./v1/codec/jsonshape/
```

## Reference

- ADR 0133; `internal/service/codec/jsonshape/CLAUDE.md`
