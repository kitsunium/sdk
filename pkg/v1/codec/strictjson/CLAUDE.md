# pkg/v1/codec/strictjson/

## Purpose

Public facade over `internal/service/codec/strictjson` (ADR 0102): decode
exactly one JSON document within a byte bound, refusing unknown and case-variant
members, duplicate names, trailing data and invalid UTF-8, with errors that
never quote the input.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Decode(r, v, maxBytes)` | func | one document from any reader; reads at most `maxBytes + 1` bytes |
| `DecodeRequest(w, req, v, maxBytes)` | func | an HTTP body: `http.MaxBytesReader`, empty body first, media type second |
| `PointerOf(err)` | func | where a refused document failed, as a JSON Pointer, bounded to `MaxPointerBytes` (256) |
| `MaxPointerBytes` | const | the pointer's bound |
| `Code*` | const | re-exported `0.3.72.*` codes |
| `DocumentTooLarge` … `DecodeMisconfigured` | var | re-exported sentinels, each with its HTTP status (413 / 400 / 415 / 500) |

## Why a package of its own

`pkg/v1/codec` blank-imports all sixteen codecs, so importing it for one strict
decoder would link BSON, CBOR, MessagePack, TOML and YAML into a server that
reads JSON bodies. This is a separate facade for that reason, the way
`pkg/v1/hash` and `pkg/v1/sign` are separate from `pkg/v1/crypto`. It is not a
codec Format: `codec.Unmarshal(codec.JSON, …)` keeps meaning what it meant.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`strictjson.go` (ADR 0008). Regenerate with `make docs-readme`; do not
hand-edit it.

## Verification

```sh
bazel test --config=race //pkg/v1/codec/strictjson:strictjson_test
cd pkg && GOWORK=off go test -race ./v1/codec/strictjson/
```
