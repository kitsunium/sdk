# internal/kernel/cache/

## Purpose

Generic, concurrency-safe **LRU + TTL cache** `Cache[K comparable, V any]` — a
kernel primitive (stdlib-only AND generic, no domain vocabulary). Admitted by
**ADR 0027**: any domain (HTTP middleware, DNS resolver, codec scratch, config
hot-reload) reaches for an LRU/TTL cache, so it belongs in the kernel alongside
`ring`/`recycler`/`snapshot`. Reuses the kernel `clock` so TTL expiry is
testable. Emits **no error codes** (`Fetch` returns `(V, bool)`).

## Contents

| File | Surface |
|---|---|
| `cache.go` | `Cache[K,V]` + `NewCache` + `Fetch`/`Set`/`SetTTL`/`Delete`/`Len`/`Purge`/`Stats` + LRU helpers |
| `entry.go` | `entry[K,V]` — intrusive doubly-linked LRU node |
| `config.go` | `Config[K,V]` — MaxEntries / DefaultTTL / Clock / OnEvict |
| `stats.go` | `StatsValue` — Hits / Misses / Evictions counters |

## Conventions

- **`Fetch`, not `Get`**: a hit mutates state (LRU promotion + counters), so the
  read verb is `Fetch` — `Get` is reserved for pure accessors (KTN-GETTER-PURE).
- **`sync.RWMutex`**: read-only `Len`/`Stats` take `RLock`; everything else `Lock`.
- **Lazy expiry**: expired entries are reaped on the next `Fetch`, not by a timer.
- **OnEvict** fires on capacity/expiry removal only — never on `Delete`/overwrite/`Purge`.
- **Injectable clock**: `Config.Clock` (nil → `clock.System`) makes TTL deterministic in tests.
- Cross-OS: 100 % portable (map/sync/time) — no OS-specific code.

## Do NOT

- Rename `Fetch` to `Get` — the LRU side effect makes a pure getter a lie.
- Add error codes — the cache has no failure surface.

## Verification

```
bazel test --config=race //internal/kernel/cache:cache_test
```
