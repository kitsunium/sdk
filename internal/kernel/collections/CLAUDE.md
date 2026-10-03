<!-- updated: 2026-10-03T03:20:00Z -->
# internal/kernel/collections/

## Purpose

The kernel's data-structure family (ADR 0155 §2, ADR 0159 §4). This directory
holds no Go code: it is a prefix, not a package, and nothing imports
`internal/kernel/collections` itself. Each member is a package of the
`internal/kernel` module with its own `CLAUDE.md`, and each still passes the
kernel's one rule — stdlib-only AND generic (`internal/kernel/CLAUDE.md`).

## The rule that put them together

A package belongs here when what it IS is a container of values, generic in
its element: it keeps them in an order (`heap`), within a bound (`ring`), or
within a bound and an age (`cache`). Each is safe for its documented callers —
`ring` for one producer and one consumer, `cache` for any number — but that is
a property of the container, not the reason it exists, which is why none of
them is in `concur/`.

## Members

| Package | What it is | In `pkg/v1` |
|---|---|---|
| `cache/` | `Cache[K, V]` — LRU and TTL on the kernel clock, `Fetch` because a hit mutates (ADR 0025) | `pkg/v1/data/cache`, beside the cache domain it underlies (ADR 0049) |
| `heap/` | `Heap[T]` — a binary heap ordered by the caller's comparison, no `container/heap` interface | `pkg/v1/collections/heap`, an alias, once ADR 0159 §4 lands |
| `ring/` | a single-producer single-consumer lock-free bounded queue (ADR 0006); the only member with codes, `0.1.3.*` | `pkg/v1/collections/ring`, once ADR 0159 §4 lands |

## Do NOT

- Put Go code in this directory. A file here would make `collections` a
  package of its own.
- Add a member for its concurrency. A primitive whose purpose is several
  goroutines — running them, deduplicating them, coalescing their work — goes
  to `concur/`.
