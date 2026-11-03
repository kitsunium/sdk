<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/data/cache/

## Purpose

Public facade for the SDK's cache, two surfaces side by side. The PRIMITIVE
(ADR 0025): the generic LRU + TTL cache of `internal/kernel/collections/cache`. The DOMAIN
(ADR 0049): the `Store[V]` port of `internal/core/data/cache` and the two stores of
`internal/service/data/cache` — a tagged, stampede-protected memory store and a tier
chain. Both are re-exported as **type aliases** (zero runtime cost) plus thin
constructor wrappers, so consumers depend only on `pkg/v1`. Stdlib-only →
dep-light; cross-OS portable.

## Surface

| Symbol | Notes |
|---|---|
| `Cache[K,V]` / `Config[K,V]` / `Stats` | type aliases onto `internal/kernel/collections/cache` (`Stats = StatsValue`) |
| `New[K,V](cfg)` | constructor (wraps `kernel/collections/cache.NewCache`) |
| `Striped[K,V]` / `NewStriped(cfg, stripes, hash)` | the striped sibling of the primitive (#808): N caches behind their own locks, the bounds divided |
| `Policy` / `PolicyLRU` / `PolicyS3FIFO` | the primitive's eviction policy (#834), also `MemoryConfig.Policy` |
| methods `Fetch`/`Set`/`SetTTL`/`Delete`/`Len`/`Purge`/`Stats` | inherited from the aliased type |
| `Store[V]` | alias of `corecache.Store[V]` — `Fetch`/`Set`/`Delete` on string keys, FROZEN at three methods (ADR 0039) |
| `EntryFetcher[V]` / `Tagger` / `Loader[V]` | siblings reached by type assertion: `FetchEntry` (value, remaining TTL, tags), `InvalidateTag`, `Load(ctx, key, fill)` |
| `Expirer` / `BulkFetcher[V]` / `BulkSetter[V]` / `Refresher[V]` / `Leaser[V]` | the L6 siblings, on both stores: `Expire(ctx)` (a caller-driven sweep, #1059), `FetchMany` / `SetMany` (#840), `LoadRefreshing` (refresh-ahead, #838), `LoadLeased(ctx, key, fill, locker)` (a lease across processes from a `lock.Locker`, #830) |
| `Absent(cause, ttl)` / `AbsentTTLOf(err)` | the mark a fill returns to have a "does not exist" answer kept as a negative entry (#838) |
| `Entry[V]` / `Fill[V]` / `NoExpiry` | aliases of `corecache.EntryValue[V]` / `corecache.Fill[V]`; `NoExpiry` is the TTL with no deadline, distinct from the zero TTL (the store's default) |
| `MemoryConfig` / `ChainConfig` | type aliases onto `internal/service/data/cache` |
| `NewMemory[V](cfg)` | the memory store; implements every sibling; a non-positive `MaxEntries` is refused, never defaulted |
| `NewWeighedMemory[V](cfg, weigh)` | the memory store bounded by `MaxBytes` too, each value measured by `weigh` (#839) |
| `NewChain[V](cfg, tiers...)` | `tiers[0]` first, the last is the authority; every tier must implement `EntryFetcher` and `Tagger` |
| `Misconfigured` / `BackendFailed` / `FillFailed` / `EntryRejected` | sentinels of `internal/core/data/cache` (`0.2.18.1`–`0.2.18.4`) |
| `ChainMisconfigured` / `TierFailed` / `LeaseFailed` | the chain's sentinels and `LoadLeased`'s (`0.3.48.1`–`0.3.48.3`), declared in `internal/core/data/cache` since ADR 0160 |

## Conventions

- **Type aliases, not new types** — `pkg/v1/data/cache.Cache[K,V]` IS the kernel type
  and `Store[V]` IS the core port.
- **`Fetch`, not `Get`** (a hit mutates LRU) — see kernel CLAUDE.md. The domain
  keeps the verb at every layer.
- **`Loader` collapses concurrent misses within ONE process**, never across
  replicas — the package doc says so before anyone sizes an origin against it.
  `Leaser` is the sibling that crosses the boundary, and what it buys depends on
  what the processes share (one fill through a shared far tier, turns over a
  memory store alone).
- The package comment is in `doc.go`, which kit writes from the design
  (`design/data/cache.yaml`, ADR 0167): edit the design, run `kit gen`, then `make api`
  and `make docs-readme`. `README.md` is written by `tools/genindex` from
  `docs/api`; do not hand-edit it.

## Do NOT

- Reimplement eviction, tagging or the chain here — the facade is aliases + the
  `New` / `NewMemory` / `NewChain` wrappers only.

## Generated

`gen_facade.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/data/cache.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `gen_facade.go`: `doc.go` holds the package comment, which kit writes from the design (ADR 0167), and the hand-written files the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `gen_facade.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/data/cache:cache_test
```
