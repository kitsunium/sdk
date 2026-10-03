<!-- updated: 2026-10-03T06:00:00Z -->
# internal/service/data/

## Purpose

The data family's engines (ADR 0155): the concrete halves of the contracts
under `internal/core/data`, at the same paths. This directory holds no Go
code: it is a prefix, not a package, and nothing imports
`internal/service/data` itself. Each member is a package of the
`internal/service` module with its own `CLAUDE.md`, and each is published by
the facade at the same path under `pkg/v1/data` — except `transform`, whose
engine `pkg/v1/data/codec` blank-imports to publish the compression frame.

## The rule that put them together

A domain belongs here when its subject is the caller's DATA itself — the bytes
a value becomes and the places those bytes are kept — rather than what the
data is for: the bytes a value is written as (`codec`) and the same bytes made
smaller (`transform`), the tables of the caller's database (`sql`) and the
documents kept above them or on a filesystem (`docstore`), the messages one
process leaves for another (`queue`), the copies kept close (`cache`) and the
files a value is published to (`vfs`).

Every codec is under `codec/`: the sixteen format packages and their 24
Formats, and the three JSON tools that are not Formats — `strictjson`,
`jsonshape`, `jsonpatch` (ADR 0155 §3). A wire format that serves one domain
stays with that domain — the WebSocket frame with `net`, the `traceparent`
header with `observe/trace` — and is not a codec.

The members compose one another and say so: `docstore` persists through
`vfs` (one overlay entry per write) or through `sql`, and `queue`'s file
broker publishes through `vfs` while its SQL broker runs on `sql` — both SQL
engines on the transaction the context carries (ADR 0139, ADR 0151). Outside
the family, `security/secret` publishes its records through `vfs`, and
`mail/spool` is a queue.

## Members

| Package | What it is | Implements | Code range | Facade |
|---|---|---|---|---|
| `cache/` | the tagged, stampede-protected memory store over `kernel/collections/cache` and the L1/L2 `NewChain`; one fill per key per PROCESS, through `kernel/concur/singleflight` (ADR 0049) | `core/data/cache` | `0.3.48.*` (plus core sentinels `0.2.18.*`) | `pkg/v1/data/cache` |
| `codec/` | sixteen format packages over 24 Format names — `asn1`, `baseenc` (nine base-N Formats), `bson`, `cbor`, `csv`, `flatbuffers`, `form`, `json`, `msgpack`, `multipart`, `ndjson`, `pem`, `tlv`, `toml`, `xml`, `yaml` — each registered at package load (ADR 0003, ADR 0156) | `core/data/codec` | `0.3.2.*` … `0.3.41.*` (one PP slot per format) | `pkg/v1/data/codec` and its per-format children (ADR 0134) |
| `codec/strictjson/` | one JSON document decoded one way, within a bound on reading — a decoder, not a Format (ADR 0102); the HTTP request body is `codec/strictjson/httpbody/`, so the decoder links no `net/http` | none — no port | `0.3.72.*` | `pkg/v1/data/codec/strictjson`, `pkg/v1/data/codec/strictjson/httpbody` |
| `codec/jsonshape/` | a Go type's wire shape under `encoding/json` — not a Format (ADR 0133) | none — no port | none | `pkg/v1/data/codec/jsonshape` |
| `codec/jsonpatch/` | two JSON documents' difference as RFC 6902 operations — not a Format (ADR 0143) | none — no port | `0.3.90.*` | `pkg/v1/data/codec/jsonpatch` |
| `docstore/` | typed, keyed JSON documents with unique and multi-valued indexes, persisted as one overlay entry per write through `vfs` (ADR 0110), or in two tables of the caller's database through `sql` (ADR 0139), with a document's versions kept in its own write (ADR 0143) | none yet — no core counterpart (ADR 0110 §D1; ADR 0160 §1 adds one) | `0.3.80.*` | `pkg/v1/data/docstore` |
| `queue/` | the file broker (its state a directory, through `vfs`), the SQL broker (one table of the caller's database, through `sql`) and the memory broker, and the `Consume` loop that sleeps on the `Waker` sibling (ADR 0054, ADR 0104, ADR 0151) | `core/data/queue` | `0.3.53.*` (plus core sentinels `0.2.23.*`) | `pkg/v1/data/queue` |
| `sql/` | the transaction manager — savepoints, the pool policy, `Join` and `Defer` — and the migration runner under a lock that dies with its holder, SQLite's on the database file's write lock (ADR 0055, ADR 0139, ADR 0140) | `core/data/sql` | `0.3.54.*` (plus core sentinels `0.2.24.*`) | `pkg/v1/data/sql` |
| `transform/` | the stdlib compressors — gzip, raw DEFLATE (`flate`) and the RFC 1950 `zlib` envelope — and the bounded decompression (ADR 0014) | `core/data/transform` | `0.3.26.*` | `pkg/v1/data/transform`, and `pkg/v1/data/codec`'s compression frame through it |
| `vfs/` | `NewOS`, confined by `os.Root`, and `NewMem`, with the five-step atomic publication (ADR 0056) | `core/data/vfs` | `0.3.55.*` (plus core sentinels `0.2.25.*`) | `pkg/v1/data/vfs` |

Every range above kept its value when its package moved (ADR 0160):
`codeRangeOwners` (`internal/kernel/errs/registry_ownership_external_test.go`)
names the new directories under the same keys, and `//:audit_sources` lists
them by their new labels.

## Do NOT

- Put Go code in this directory. A file here would make `data` a package of
  its own, and the family a domain nobody chose.
- Reimplement a primitive here because a data engine needs it: a lock goes to
  `lock`, a wait to `kernel/clock`, a recycled buffer to
  `kernel/concur/recycler` or `core/data/codec/scratch`.
- Import one member from another without saying so in both `CLAUDE.md` files:
  today `docstore` and `queue` reach `sql` and `vfs`, and no other member
  reaches a sibling.
