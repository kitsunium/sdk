<!-- updated: 2026-10-03T03:20:00Z -->
# internal/kernel/concur/

## Purpose

The kernel's concurrency family (ADR 0155 §2, ADR 0159 §4). This directory
holds no Go code: it is a prefix, not a package, and nothing imports
`internal/kernel/concur` itself. Each member is a package of the
`internal/kernel` module with its own `CLAUDE.md`, and each still passes the
kernel's one rule — stdlib-only AND generic (`internal/kernel/CLAUDE.md`).

## The rule that put them together

A primitive belongs here when what it is FOR is several goroutines: it runs
them and waits for them (`worker`, `group`), lets them share one execution or
one value (`singleflight`, `snapshot`), coalesces what they hand it
(`batcher`), or lets them reuse objects instead of allocating them
(`recycler`, `buffer`).

Being safe to call from several goroutines is not enough: `collections/cache`
and `collections/ring` are, and they live in `collections/` because what they
ARE is a container. `buffer` is here and not at the root because it is not a
primitive of its own kind — it is `recycler.CappedPool[*[]byte]` with a
byte-slice threshold, so it sits beside the primitive it specialises.

## Members

| Package | What it is | In `pkg/v1` |
|---|---|---|
| `batcher/` | `Batcher[T]` — coalesce, then flush on size or on a ticker that runs on the injected `Config.Clock` (ADR 0014) | `pkg/v1/concur/batcher`, an alias, once ADR 0159 §4 lands |
| `buffer/` | the pooled `*[]byte` the logger and the network server format into — `recycler.CappedPool` specialised for bytes (ADR 0010) | not on its own (ADR 0159 §4): reached through the domains that use it |
| `group/` | structured concurrency — `Go` / `Wait` / `Collect`, the first error or every error joined (`NewJoined`), a bounded parallelism, a child's panic delivered to the waiter | `pkg/v1/concur/group`, once ADR 0159 §4 lands |
| `recycler/` | `Pool[T]` and `CappedPool[T]` over `sync.Pool` — reuse, reset, and a capacity past which an object is dropped (ADR 0010) | `pkg/v1/concur/recycler`, once ADR 0159 §4 lands |
| `singleflight/` | `Group[K, V]` — one execution per key however many callers arrive, a panic re-raised in every waiter (ADR 0049) | `pkg/v1/concur/singleflight`, once ADR 0159 §4 lands |
| `snapshot/` | `Value[T]` — copy-on-write: lock-free reads, serialised writers (ADR 0011) | `pkg/v1/concur/snapshot`, once ADR 0159 §4 lands |
| `worker/` | `LoopDaemon` — a goroutine started and stopped; `Every` ticks it on an injected clock (`WithClock`) and can end it with its owner (`WithDone`) | `pkg/v1/concur/worker`, once ADR 0159 §4 lands |

## Do NOT

- Put Go code in this directory. A file here would make `concur` a package of
  its own, and the family a domain nobody chose.
- Add a member because it is goroutine-safe. Ask what it IS: a container goes
  to `collections/`, and a primitive no family describes stays at the kernel's
  root.
