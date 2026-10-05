<!-- updated: 2026-10-05T00:00:00Z -->
# pkg/v1/data/

## Purpose

The data family's public facades (ADR 0155). This directory holds no Go code:
it is a prefix, not a package, and there is no
`github.com/kitsunium/sdk/pkg/v1/data` to import. Each member is a package of
the SDK module with its own `CLAUDE.md`, `README.md` (written by `tools/genindex`
from `docs/api`, ADR 0167) and, where it measures something, `BENCH.md`.
A member is imported by its full path —
`github.com/kitsunium/sdk/pkg/v1/data/codec` — and links what it imports and
never this directory or its siblings, because Go links a package's imports and
not its parent (the measurement ADR 0155 records): `codec/json` does not link
`codec`, and `codec` alone links every format.

## The rule that put them together

A domain belongs here when its subject is the caller's DATA itself — the bytes
a value becomes and the places those bytes are kept — rather than what the
data is for: the bytes a value is written as (`codec`, every format and the
JSON tools in one tree — ADR 0155 §3), the tables of the caller's database
(`sql`) and the documents kept above them or on a filesystem (`docstore`), the
messages one process leaves for another (`queue`), the copies kept close
(`cache`) and the files a value is published to (`vfs`). Compression is
`transform`: the same bytes made smaller, a registry of its own beside the
codec's, which `codec`'s compression frame goes through.

`semver` sits here as the reorganisation's target tree places it: a version
string is data a program compares. ADR 0155 §1 and ADR 0159 §4 wrote it at the
root of `pkg/v1` beside `errs` and `clock`; the kernel package it forwards to
stays at the kernel's root, `internal/kernel/semver`.

## Members

| Package | What it publishes | Aliases onto | README |
|---|---|---|---|
| `cache/` | the LRU+TTL primitive `New` → `Cache[K,V]` (ADR 0025) and the domain `NewMemory` / `NewChain` → a `Store[V]` with tag invalidation and in-process stampede protection (ADR 0049) | `internal/kernel/collections/cache`, `internal/core/data/cache`, `internal/service/data/cache` | `cache/README.md` |
| `codec/` | `Marshal` / `Unmarshal` / `NewEncoder` / `NewDecoder` over a `Format` registry with every format registered — the aggregate of the sixteen per-format packages beneath it — and the self-describing compression frame (ADR 0003, ADR 0014) | `internal/core/data/codec`, `internal/core/data/transform` | `codec/README.md` |
| `codec/{asn1,baseenc,bson,cbor,csv,flatbuffers,form,json,msgpack,multipart,ndjson,pem,tlv,toml,xml,yaml}/` | one format registered alone — a blank import registers that codec and links no other (ADR 0134) — with its `Format` names and its error codes; `codec/` is their aggregate; `bson` also names BSON's value types, `multipart` its `Form` / `Part` | the format's engine under `internal/service/data/codec`, its codes under `internal/core/data/codec` | each child's `README.md` |
| `codec/strictjson/` | `Decode` — one JSON document read one way, within a bound on reading (ADR 0102); links no `net/http` | `internal/core/data/codec/strictjson`, `internal/service/data/codec/strictjson` | `codec/strictjson/README.md` |
| `codec/strictjson/httpbody/` | `DecodeRequest` — the JSON body of an HTTP request, through `http.MaxBytesReader`, empty body first, media type second | `internal/service/data/codec/strictjson/httpbody` | `codec/strictjson/httpbody/README.md` |
| `codec/jsonshape/` | `Of` / `For[T]` → a type's wire shape under `encoding/json` (ADR 0133) | `internal/service/data/codec/jsonshape` | `codec/jsonshape/README.md` |
| `codec/jsonpatch/` | `Diff` → two JSON documents' difference as RFC 6902 operations (ADR 0143) | `internal/service/data/codec/jsonpatch` | `codec/jsonpatch/README.md` |
| `docstore/` | `Open` → a `Store[T]` over a `vfs` filesystem and `OpenSQL` → an `SQLStore[T]` over the caller's database, with unique and multi-valued indexes and versions (ADR 0110, ADR 0139, ADR 0143); the ports a double stands in for (ADR 0160) | `internal/core/data/docstore`, `internal/service/data/docstore` | `docstore/README.md` |
| `queue/` | `NewFile` / `NewSQL` / `NewMemory` → a `Broker`, and `Consume` (ADR 0054, ADR 0104, ADR 0151) | `internal/core/data/queue`, `internal/service/data/queue` | `queue/README.md` |
| `semver/` | `IsValid` / `Compare` / `Prerelease` and `IsPseudoVersion` / `PseudoVersionRev` / `PseudoVersionTime` (ADR 0156 §4) | `internal/kernel/semver` | `semver/README.md` |
| `sql/` | `NewTransactor` / `NewChecker` / `NewMigrator` over `database/sql`, no driver and no ORM (ADR 0055, ADR 0139, ADR 0140) | `internal/core/data/sql`, `internal/service/data/sql` | `sql/README.md` |
| `transform/` | `Compress` / `Decompress` / `DecompressBounded` / `Lookup` over the `Algorithm` registry, with `gzip`, `flate` and `zlib` registered by the import — compression without the codec package (ADR 0014) | `internal/core/data/transform`, `internal/service/data/transform` | `transform/README.md` |
| `vfs/` | `NewOS` / `NewMem` → a `FullFS`, reading through `io/fs` unchanged and publishing atomically (ADR 0056) | `internal/core/data/vfs`, `internal/service/data/vfs` | `vfs/README.md` |

`docstore` and `queue` name the `sql` facade's aliases in their SQL
signatures — `sql.Dialect`, `sql.Migration` — so a consumer's documentation
never shows an internal package: the one sibling import in the family, which
both packages' `CLAUDE.md` files name.

Each that existed before moved here from `pkg/v1/<name>` in one minor
release, with no alias left at the old path — a clean break, permitted only
while the module is v0 (ADR 0155 §4, extending ADR 0040). The per-format children, `transform` and
`codec/strictjson/httpbody` are new in the same release and had no earlier path.

## Do NOT

- Put Go code in this directory. A file here would publish a package nobody
  designed, at a path every member would then appear to belong to.
- Hand-edit a member's `README.md`: edit its doc comment and run
  `make docs-readme`, whose `--repository.path` names the member's full path.
- Leave an alias package at an old `pkg/v1/<name>` path. ADR 0155 §4 refuses
  it: it would double the surface for consumers nobody can name.
- Call `Register` from a facade. A format's engine registers itself once, at
  package load, whoever imports it — `codec` and the per-format child reach
  the same registration — and a second registration of a `Format` panics at
  boot.
