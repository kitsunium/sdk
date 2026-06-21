# ADR 0021 — BSON codec (M5)

- **Status**: Accepted
- **Date**: 2026-06-21
- **Deciders**: SDK maintainers
- **Related**: ADR 0003 (universal codec package, M5 deferred set), ADR 0005/0006 (error-code registry)

## Context

ADR 0003 §M5 deferred a set of additional wire formats. BSON (MongoDB's binary
document format, RFC-less but stable and widely used for document storage and
inter-service payloads) is a natural M5 addition: it slots behind the existing
universal `Marshal(F, v)` / `Unmarshal(F, data, &v)` dispatch with no verb
change.

BSON has no standard-library implementation. The de-facto Go library is
`go.mongodb.org/mongo-driver/bson`, which can be imported standalone (the
`bson` sub-package does not pull the MongoDB client/driver machinery — the
added transitive surface is small, verified at `v1.17.4`).

## Decision

1. **Add `internal/service/codec/bson`** wrapping `go.mongodb.org/mongo-driver/bson`,
   registering the Format `"bson"` (MIME `application/bson`, extension `.bson`).
2. **Placement follows the library-backed codec precedent.** Like `cbor`
   (fxamacker), `msgpack` (vmihailenco), `toml` (pelletier) and `yaml`
   (gopkg.in/yaml.v3), the third-party dependency lives in
   `internal/service/go.mod` — NOT quarantined under `third-party/`. The
   `third-party/` quarantine (ADR 0012/0015) is reserved for the **root
   module's** heavy vendor integrations (AWS/DB writers); the codec layer
   already carries four single-purpose codec libraries and BSON joins them.
3. **Capabilities**: `Appender` yes (Marshal + append; the library exposes no
   in-place API). `StreamingCodec` **no** — mongo-driver does not expose a
   stable incremental `io` Encoder/Decoder, so BSON is non-streaming like
   `csv`/`asn1`/`pem`.
4. **Error block `0.3.36.*`** (next free `PP` octet — 0.3.25..0.3.35 are taken
   by the third-party AWS/DB writers; verified against `//:audit_sources`).
   Codes: `BSON_MARSHAL_FAILED` (`.1`), `BSON_UNMARSHAL_FAILED` (`.2`),
   `BSON_SIZE_EXCEEDED` (`.3`).
5. **Hardening**: a 10 MiB `maxBSONBytes` Unmarshal cap (CWE-400), matching the
   other library-backed codecs — mongo-driver reads declared element lengths
   before validating them, so the size cap is the primary memory-exhaustion
   defence.

## Consequences

- `pkg/v1/codec` gains the `BSON` Format constant; the registry now covers
  **22 Format names across 14 codec packages**. Docs (counts + tables across
  baseenc/service/core/root/pkg CLAUDE.md) and the `codec_test` bijections
  (`expectedAppenders`, `codecAdapters`, the Append round-trip arm) are updated
  per CLAUDE.md rule 11.
- **Top-level must be a document.** BSON cannot represent a bare scalar at the
  root, so `Marshal` of a top-level scalar returns `BSON_MARSHAL_FAILED`. This
  is a documented constraint (the `bson` round-trip fixtures use a document map,
  not the universal `complexRT`, which additionally carries `uint64` and
  sub-millisecond time that BSON does not preserve losslessly).
- `internal/service/go.sum` grows by the (small) `mongo-driver/bson` transitive
  set; `MODULE.bazel` `go_deps` + `bazel mod tidy` resolve it. Consumers of
  `pkg/v1/codec` that never import BSON still link it (blank-import activates the
  whole registry) — acceptable given the small footprint, consistent with the
  existing four codec libraries.

## Why not

- **Quarantine under `third-party/`** — rejected: `mongo-driver/bson` standalone
  is light (comparable to cbor/msgpack), and quarantining would break the
  uniform "one codec package per format under `internal/service/codec`" shape
  for no real dependency-weight saving. Heavy clients (the full mongo driver)
  would justify quarantine; the `bson` sub-package does not.
- **mongo-driver/v2** — the v1 line is deprecated upstream in favour of v2, but
  v1 `bson` is stable, widely deployed, and lighter; a v2 migration is a
  drop-in follow-up if needed.

## References

- ADR 0003 §M5 (deferred formats), ADR 0005/0006 (code registry), ADR 0012/0015
  (third-party quarantine policy this codec deliberately does NOT use).
