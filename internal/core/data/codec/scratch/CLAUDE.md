<!-- updated: 2026-11-03T12:00:00Z -->
# internal/core/data/codec/scratch/

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
`core/data/codec`, consumed only by `service/data/codec/*` below `core` in the layer
graph. (Before ADR 0010 each codec hand-rolled its own `sync.Pool`; the
mechanism was duplicated, the thresholds drifted.)

## Surface

| Symbol | Contract |
|---|---|
| `MaxRetainedBufBytes` | `const = 256 << 10`. Buffers larger than this are dropped on release, never re-pooled. |
| `AcquireBuffer() *bytes.Buffer` | Returns an **already-Reset** buffer from the shared pool. Caller owns it until `ReleaseBuffer`. |
| `ReleaseBuffer(*bytes.Buffer)` | Repools the buffer unless `Cap() > MaxRetainedBufBytes` (then orphaned for the GC). |
| `DetachBuffer(*bytes.Buffer) []byte` | Hands the caller the encoded bytes and ends its ownership of the buffer: within the cap, a clone and a `ReleaseBuffer`; over it, the buffer's own storage, orphaned without a copy or a reset. Nil-safe. |
| `MaxRetainedSizedBytes` | `const = 4 << 20` (#906). The size-classed tier keeps buffers up to it; past it, dropped. |
| `AcquireBufferSized(hint int) *bytes.Buffer` | A clean buffer with `Cap() ≥ hint`: the shared pool within 256 KiB, else the 1 MiB class (≤ 1 MiB) or the 4 MiB class, whose new buffers are allocated at the class's full size so one serves every hint of its class. Caller owns it until `ReleaseBufferSized`. |
| `ReleaseBufferSized(*bytes.Buffer)` | Routes by capacity: shared pool ≤ 256 KiB, 1 MiB class, 4 MiB class, dropped past 4 MiB. Kept apart from `ReleaseBuffer`, which every codec's hot path inlines (cost 67; with the routing it was 211 and kit gen refused it). |
| `AcquireReader(src []byte) *bytes.Reader` | Returns a `*bytes.Reader` positioned at `src`. Caller owns it until `ReleaseReader`; `src` must stay alive + unmodified while the reader is used. |
| `ReleaseReader(*bytes.Reader)` | Repools the reader. No cap-discard — a `bytes.Reader` is a fixed-size struct. |
| `AppendWriter` | An `io.Writer` over a caller's slice: `Write` appends in place (never fails), `Bytes()` returns the slice so far, `Truncate()` cuts it back to its length at acquisition — the Appender contract on a failed encode (#1124). |
| `AcquireAppendWriter(dst []byte) *AppendWriter` | A pooled writer appending to `dst`. Pooled because a standard-library encoder takes it as an `io.Writer`, which moves it to the heap: the pool keeps an `Append` at its encoder's own allocations. |
| `ReleaseAppendWriter(*AppendWriter)` | Repools the writer after dropping the slice it held, so an idle writer pins no caller's buffer. Nil-safe. |

## Lifetime contract (non-negotiable)

A value from `AcquireBuffer` is caller-owned until the matching
`ReleaseBuffer`. After release, the buffer **and any slice aliasing
`buf.Bytes()`** must not be used. If the encoded bytes must outlive the
release, clone them first (`slices.Clone(buf.Bytes())`) or call
`DetachBuffer`, the size-aware release the codecs share (clone on the small
path, orphan-without-clone on the over-cap path) — `csv`, `multipart`, `ndjson`
and `pem` each carried a copy of it before it moved here.

## The size-classed tier (#906)

Every codec rented the one shared pool and orphaned any buffer that grew past
256 KiB, so a workload of 300 KB – 1 MB documents paid the whole growth chain
on every call: bson's `Marshal/batch1000` (~258 KB, a buffer doubled to
512 KiB) allocated 852 KiB in 13 allocations per op. The tier adds two pools
of `recycler.CappedPool[*bytes.Buffer]` above the shared one, by capacity —
up to 1 MiB, up to 4 MiB — each discarding what is past its ceiling; their
retention is `sync.Pool`'s, drained over two collections, which is the bound
on what they pin (the recycler has no count cap). Only a codec that asks for
a size uses it: `AcquireBufferSized(hint)` / `ReleaseBufferSized`. bson's
`Marshal` passes the capacity the last document needed when the shared pool
could not keep it, and swaps a backing array it grew into the buffer it
releases; its large-document Marshal is one allocation — the caller's copy —
in steady state (`TestAllocBudget`, `BENCH.md` there). `DetachBuffer` keeps
the small-tier copy rule: past 256 KiB it hands the storage over rather than
clone a large payload for a pool its caller never asked for. A warm
`AcquireBufferSized` round trip allocates nothing (budget `AcquireBufferSized`,
`allocs: 0`, in `design/sdk.yaml`).

## Why one shared pool

`sync.Pool` is internally per-P sharded, so a single shared pool does not
add contention versus nine independent pools — and it yields a higher
reuse rate because any codec's released buffer can serve any other codec's
next call. The victim-cache semantics (survive one GC, drain after two)
are unchanged.

## Consumers

`AcquireBuffer` / `ReleaseBuffer`: `service/data/codec/{bson,cbor,msgpack,multipart,ndjson,tlv,toml,xml,yaml}`,
the `baseenc` JSON-mediation buffer and `third-party/codec/protobuf`'s stream
encoder — eleven consumers (`csv` and `pem` write into their result directly
since #1124, `json` through an `AppendWriter`; `msgpack` and `tlv` also borrow
one to sort a map's keys, #1104). `AcquireBufferSized` / `ReleaseBufferSized`:
`service/data/codec/bson`'s `Marshal` (#906). `DetachBuffer`:
`service/data/codec/{multipart,ndjson}`, whose `Marshal` returns the
bytes it encoded into a pooled buffer. `AcquireReader` /
`ReleaseReader`: `service/data/codec/csv` (its `Unmarshal` wraps the input `[]byte` in
a recyclable `*bytes.Reader`). The `≥2-consumer` rule for a shared primitive
is satisfied many times over.

`AcquireAppendWriter` / `ReleaseAppendWriter`: `service/data/codec/{json,xml}`,
whose `Append` hands the caller's buffer to `encoding/json/v2.MarshalWrite` /
`encoding/xml.NewEncoder` instead of encoding into a pooled buffer and copying
(#1124), and `ndjson`'s stream encoder, which writes each line through one into
its own line buffer. `csv` and `pem` need no writer: their encoders append to
`dst` themselves.

The two additions of 2026-11-03 compose: a codec encoding into a size-class
buffer may lend its room (`AvailableBuffer`) to an `AppendWriter`, the writes
landing in the class's array while they fit. `sized_appendwriter_external_test.go`
holds that shape — in place, `Truncate` back to the prefix, a clean buffer
from the class afterwards — and runs it from several goroutines across every
tier under `-race`.

`MaxRetainedBufBytes` as a ceiling of its own: `service/data/codec/tlv`. Its streaming encoder grows a
raw `*[]byte` by `append`, a shape `AcquireBuffer` cannot serve without a copy,
so it keeps its own `recycler.CappedPool[*[]byte]` — and passes it this
constant as the ceiling instead of declaring a second `256 << 10`, which is
what the Do NOT below asks of a codec with its own pool.

## Do NOT

- Reset the buffer yourself before use — `AcquireBuffer` already did.
- Return a buffer to the pool whose `Bytes()` you handed to a caller
  without cloning first.
- Add a second cap-discard constant in a codec — read
  `scratch.MaxRetainedBufBytes`.

## Performance contracts

`appendwriter_integration_test.go` (race off) pins a warm `AcquireAppendWriter`
/ `Write` / `ReleaseAppendWriter` round trip into a `dst` with room at zero
allocations.

The design budgets four calls (ADR 0165): a warm `AcquireBuffer` /
`ReleaseBuffer` round trip, a warm `AcquireBufferSized` / `ReleaseBufferSized`
one and a warm `AcquireReader` / `ReleaseReader` one allocate nothing, and `DetachBuffer` of a buffer under the cap allocates
exactly once — the caller's clone. `perf_fixtures_test.go` (race off) sets
each call up; `gen_perf_test.go`, written by kit gen, counts it; the alloc
lane runs both.

## Verification

```
bazel test --config=race //internal/core/data/codec/scratch:scratch_test
bazel test --config=alloc //internal/core/data/codec/scratch:scratch_test
```
