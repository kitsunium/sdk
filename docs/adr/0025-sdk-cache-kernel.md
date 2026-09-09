# ADR 0025 — Generic LRU+TTL cache as a kernel primitive (`cache`)

- **Status**: Accepted — **amended by [ADR 0049](0049-cache-becomes-a-domain.md)**
- **Date**: 2026-06-24
- **Deciders**: SDK maintainers
- **Related**: ADR 0010 (recycler), ADR 0011 (snapshot), ADR 0006 (ring) — the kernel-primitive precedents; ADR 0024 (Phase-B wave)

> **Amendment (2026-09-09, [ADR 0049](0049-cache-becomes-a-domain.md)).** The
> placement decided below stands: `Cache[K,V]` remains a kernel primitive and
> nothing about it moved. ADR 0049 adds a DOMAIN *above* it —
> `internal/core/cache` (`Store[V]` + siblings, block `0.2.18.*`) and
> `internal/service/cache` (tagged memory store + L1/L2 chain, block
> `0.3.48.*`) — carrying the three things a primitive deliberately does not
> have: invalidation by tag, stampede protection, and tier chaining. It also
> notes, as a fact checked rather than assumed, that this package had **zero**
> in-tree consumers besides `pkg/v1/cache` between the two ADRs.

## Context

Phase B calls for a `cache`. Unlike `id`/`metrics`/`resilience`/`config` (which
carry domain vocabulary and become core siblings), a generic `Cache[K,V]` LRU +
TTL is **domain-neutral**: an HTTP middleware, a DNS resolver, codec scratch, or
hot-reloaded config all reach for the same mechanism. By the kernel gate
("stdlib-only AND generic, no domain vocabulary"), it belongs in
`internal/kernel`, alongside `ring`/`recycler`/`snapshot`.

## Decision

1. **Add `internal/kernel/cache`** — `Cache[K comparable, V any]` with
   `NewCache(Config)`, `Fetch`/`Set`/`SetTTL`/`Delete`/`Len`/`Purge`/`Stats`.
   LRU via an intrusive doubly-linked list; TTL via the injectable kernel
   `clock` (lazy expiry on `Fetch`). `sync.RWMutex` (read-only `Len`/`Stats`
   take `RLock`). `Config` carries `MaxEntries`/`DefaultTTL`/`Clock`/`OnEvict`.
2. **`Fetch`, not `Get`** — a hit mutates state (LRU promotion + counters), so
   the read verb is `Fetch`; `Get` is reserved for pure accessors (KTN-GETTER-PURE).
3. **No error codes** — the cache has no failure surface (`Fetch` returns
   `(V, bool)`; bad options clamp to sane defaults). It therefore imports no
   `errs` and needs no `audit_srcs`.
4. **`pkg/v1/cache`** re-exports via generic type aliases (`Cache[K,V] =
   kernel/cache.Cache[K,V]`, `Stats = StatsValue`) + a `New` wrapper.

## Consequences

- An 8th kernel package; the kernel inventory table + `internal/kernel/CLAUDE.md`
  audit ("who stays") gain a `cache` row. `pkg/v1` gains a dep-light `cache`
  facade (stdlib only). Docs updated per rule 11.
- `Cache` does NOT use `snapshot` (that is for read-mostly immutable maps; an LRU
  is mutable). It DOES reuse `clock`. The earlier "snapshot/recycler reuse"
  sketch was aspirational; the honest reuse is `clock`. Entry pooling via
  `recycler` is a deferred optimization (the `Fetch` hit path already allocates
  nothing).

## Alternatives considered

- **core sibling** — rejected: a generic cache has no domain vocabulary; the
  kernel gate places it with `ring`/`recycler`/`snapshot`.
- **`Get` verb** — rejected: KTN-GETTER-PURE forbids a mutating getter, and an
  LRU read mutates order; `Fetch` is honest.
- **recycler-backed entries in v1** — deferred: the hit path is already
  alloc-free; pooling helps only Set churn and adds clear-on-Put complexity.

## Deferred

- `recycler.Pool[*entry]` to cut Set-time allocation churn.
- A `snapshot`-backed read-mostly cache variant (frozen maps).
- Active TTL sweeper (`worker.Every`) instead of purely lazy expiry.

## References

- Impl: `internal/kernel/cache/`, `pkg/v1/cache/`.
- ADR 0010/0011/0006 (kernel primitives), ADR 0024 (Phase-B wave).
