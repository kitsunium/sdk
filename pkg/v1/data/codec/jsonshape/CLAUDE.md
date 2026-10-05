<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/data/codec/jsonshape/

## Purpose

Public facade for describing how values of a Go type look on the wire under
`encoding/json` (ADR 0133): kinds, members, which may be missing or null, and
the Go field behind each member. Aliases onto `internal/service/data/codec/jsonshape`
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
text. It has its own facade for `strictjson`'s reason: `pkg/v1/data/codec`
blank-imports every codec, and describing a type links none of them.

## Do NOT

- Add logic here; it belongs in `internal/service/data/codec/jsonshape`.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/data/codec.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: `doc.go` holds the package comment, which kit writes from the design (ADR 0167), and the hand-written files the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/data/codec/jsonshape:jsonshape_test
cd pkg && GOWORK=off go test -race ./v1/data/codec/jsonshape/
```

## Reference

- ADR 0133; `internal/service/data/codec/jsonshape/CLAUDE.md`
