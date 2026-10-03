<!-- updated: 2026-10-03T00:00:00Z -->
# pkg/v1/data/codec/strictjson/httpbody/

## Purpose

Public facade over `internal/service/data/codec/strictjson/httpbody`: the JSON
body of an HTTP request decoded by the strict decoder of
`pkg/v1/data/codec/strictjson` (ADR 0102), plus what only an HTTP body has — a
bound `net/http` enforces too, an empty body settled first, a media type that
must declare JSON.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `DecodeRequest(w, req, v, maxBytes)` | func | `http.MaxBytesReader`, empty body first (`CodeDocumentEmpty`), media type second (`CodeMediaTypeUnsupported`, 415), then strictjson's `Decode` |

The refusals are strictjson's — its codes, its sentinels and its `PointerOf`;
this package declares none, and a handler imports both.

## Why a package of its own

So that `pkg/v1/data/codec/strictjson` links no `net/http`. `DecodeRequest`
lived there until a program decoding documents that are not request bodies was
found carrying an HTTP stack for one function. It moved here, a child of
`strictjson` that links it — the reverse is never true (ADR 0155). The move
broke the import path while v0 (ADR 0155 §4); `framework/internal/kit` moved
with it.

## Do NOT

- Add a code, a sentinel or a second decoder here: the refusals are
  strictjson's, so a body is refused exactly as a document is.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Verification

```sh
bazel test --config=race //pkg/v1/data/codec/strictjson/httpbody:httpbody_test
cd pkg && GOWORK=off go test -race ./v1/data/codec/strictjson/httpbody/
```
