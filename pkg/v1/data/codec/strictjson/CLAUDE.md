# pkg/v1/data/codec/strictjson/

## Purpose

Public facade over `internal/service/data/codec/strictjson` (ADR 0102): decode
exactly one JSON document within a byte bound, refusing unknown and case-variant
members, duplicate names, trailing data and invalid UTF-8, with errors that
never quote the input.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Decode(r, v, maxBytes)` | func | one document from any reader; reads at most `maxBytes + 1` bytes |
| `PointerOf(err)` | func | where a refused document failed, as a JSON Pointer, bounded to `MaxPointerBytes` (256) — a refused request body included |
| `MaxPointerBytes` | const | the pointer's bound |
| `Code*` | const | re-exported `0.3.72.*` codes, declared in `internal/core/data/codec/strictjson` (ADR 0160) |
| `DocumentTooLarge` … `DecodeMisconfigured` | var | re-exported sentinels, each with its HTTP status (413 / 400 / 415 / 500), aliased from the core |

## Why a package of its own

`pkg/v1/data/codec` blank-imports all sixteen codecs, so importing it for one strict
decoder would link BSON, CBOR, MessagePack, TOML and YAML into a server that
reads JSON bodies. This is a separate facade for that reason — a child of
`pkg/v1/data/codec` that does not link it, because Go links what a package imports
and never its parent directory, exactly as `pkg/v1/crypto/hash` and
`pkg/v1/crypto/sign` are children of `pkg/v1/crypto` that do not link it
(ADR 0155). It is not a codec Format: `codec.Unmarshal(codec.JSON, …)` keeps
meaning what it meant.

## Why the request body is `httpbody/`

`DecodeRequest` lived here until it was the one reason this package linked
`net/http`: a program decoding documents from files, queues or sockets carried
an HTTP stack it never served. It is `httpbody/` now — a child, which links
this package; this package does not link it (`TestTheDecoderLinksNoHTTP` asks
`go list -deps`). A server imports both: `httpbody.DecodeRequest` for the body,
this package's codes and `PointerOf` for the refusals. Moving it was a v0
break of the import path (ADR 0155 §4); the framework moved with it.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`strictjson.go` (ADR 0008). Regenerate with `make docs-readme`; do not
hand-edit it.

## Generated

`facade_gen.go` is kit's (ADR 0165): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/data/codec/strictjson.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the package comment and the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```sh
bazel test --config=race //pkg/v1/data/codec/strictjson:strictjson_test
cd pkg && GOWORK=off go test -race ./v1/data/codec/strictjson/...
```

## Subtree

- `httpbody/` — the JSON body of an HTTP request — see `httpbody/CLAUDE.md`
