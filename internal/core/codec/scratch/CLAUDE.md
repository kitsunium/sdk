# internal/core/codec/scratch/

## Purpose

Shared, size-bounded recycling primitives for the service codecs. Before
this package, every codec that encoded into a transient `*bytes.Buffer`
carried its own `sync.Pool`, its own `256 << 10` cap-discard constant, and
its own `releaseBuffer` helper — nine byte-for-byte copies of the same
logic that could silently drift apart. `scratch` is the single source of
truth for the cap-discard policy and the shared pool.

## Why core, not kernel

Since ADR 0010 the recycling MECHANISM is shared: `bufferPool` is a
`recycler.CappedPool[*bytes.Buffer]` and `readerPool` a plain
`recycler.Pool[*bytes.Reader]`. What stays here is the codec VOCABULARY —
the 256 KiB codec-payload threshold (`MaxRetainedBufBytes`) and the concrete
`*bytes.Buffer` / `*bytes.Reader` types — which is why this package lives in
`core/codec`, consumed only by `service/codec/*` below `core` in the layer
graph. (Before ADR 0010 each codec hand-rolled its own `sync.Pool`; the
mechanism was duplicated, the thresholds drifted.)

## Surface

| Symbol | Contract |
|---|---|
| `MaxRetainedBufBytes` | `const = 256 << 10`. Buffers larger than this are dropped on release, never re-pooled. |
| `AcquireBuffer() *bytes.Buffer` | Returns a **already-Reset** buffer from the shared pool. Caller owns it until `ReleaseBuffer`. |
| `ReleaseBuffer(*bytes.Buffer)` | Repools the buffer unless `Cap() > MaxRetainedBufBytes` (then orphaned for the GC). |
| `AcquireReader(src []byte) *bytes.Reader` | Returns a `*bytes.Reader` positioned at `src`. Caller owns it until `ReleaseReader`; `src` must stay alive + unmodified while the reader is used. |
| `ReleaseReader(*bytes.Reader)` | Repools the reader. No cap-discard — a `bytes.Reader` is a fixed-size struct. |

## Lifetime contract (non-negotiable)

A value from `AcquireBuffer` is caller-owned until the matching
`ReleaseBuffer`. After release, the buffer **and any slice aliasing
`buf.Bytes()`** must not be used. If the encoded bytes must outlive the
release, clone them first (`slices.Clone(buf.Bytes())`) or use the
size-aware detach pattern the codecs already implement (clone on the small
path, orphan-without-clone on the over-cap path).

## Why one shared pool

`sync.Pool` is internally per-P sharded, so a single shared pool does not
add contention versus nine independent pools — and it yields a higher
reuse rate because any codec's released buffer can serve any other codec's
next call. The victim-cache semantics (survive one GC, drain after two)
are unchanged.

## Consumers

`BufferPool`: `service/codec/{cbor,csv,json,msgpack,multipart,ndjson,pem,toml,xml,yaml}`
plus the `baseenc` JSON-mediation buffer — eleven consumers. `ReaderPool`:
`service/codec/{csv,msgpack}` (their `Unmarshal` wraps the input `[]byte` in
a recyclable `*bytes.Reader`). The `≥2-consumer` rule for a shared primitive
is satisfied many times over.

## Do NOT

- Reset the buffer yourself before use — `AcquireBuffer` already did.
- Return a buffer to the pool whose `Bytes()` you handed to a caller
  without cloning first.
- Add a second cap-discard constant in a codec — read
  `scratch.MaxRetainedBufBytes`.

## Verification

```
bazel test --config=race //internal/core/codec/scratch:scratch_test
```
