<!-- updated: 2026-05-18T14:30:00Z -->
# internal/kernel/

## Purpose

The SDK's lowest layer: **stdlib-only AND generic** primitives. A package qualifies for `kernel/` only if both halves are true — it imports nothing outside the Go standard library AND its vocabulary is domain-neutral enough that any future domain could plausibly import it. The 2026-04-19 layer audit (`.claude/contexts/sdk-layer-placement-audit.md`) is the canonical reference; it documents why `level` was relocated OUT and which packages remain.

## Contents

| Package | Purpose | Code range |
|---|---|---|
| `errs/` | SDK-wide typed error + dotted-quad registry (ADR 0005) | `0.0.0.*` (meta-codes 0.0.0.1-6, documentary only) |
| `recycler/` | generic `Pool[T]` + `CappedPool[T]` object pools (ADR 0010) | (none — panics on programmer error) |
| `snapshot/` | generic `Value[T]` copy-on-write container (ADR 0011) | (none — never returns errors) |
| `buffer/` | `sync.Pool` of `[]byte` — a `recycler.CappedPool[*[]byte]` specialisation | `0.1.1.*` (reserved) |
| `clock/` | time port, split in two: `Clock` (`Now` + `Since`) and `Waiter` (`After`/`NewTimer`/`NewTicker`/`Sleep`), joined by `Timed`; `System` delegates to package `time`, `ManualClock` is the deterministic test double | `0.1.2.*` (reserved — none emitted, none planned: bad input panics) |
| `ring/` | SPSC lock-free bounded queue (ADR 0006) | `0.1.3.*` (RING_FULL / RING_EMPTY / RING_CAP_ZERO emit today) |
| `batcher/` | generic `Batcher[T]` coalesce/flush/ticker buffer (ADR 0014) | `0.1.5.*` (BATCHER_CLOSED / BATCHER_DELIVER_FAILED) |
| `worker/` | generic goroutine-lifecycle daemon (`LoopDaemon`, `Start`/`Every`/`Stop`) | (none — emits no codes) |
| `cache/` | generic `Cache[K,V]` LRU + TTL cache (ADR 0025); reuses `clock` for testable expiry | (none — `Fetch` returns `(V, bool)`) |
| `singleflight/` | generic `Group[K,V]` call deduplication (ADR 0049); one execution per key however many callers arrive | (none — transparent to `fn`'s error; a panic is re-raised, not coded) |

Each package owns a sibling `CLAUDE.md` documenting its surface and contract.

## Module

Single module `github.com/kitsunium/sdk/internal/kernel` — one `go.mod`, one `go.sum`, shared test harness. Each subdirectory is a Go package (not a sub-module). Build with `cd internal/kernel && GOWORK=off go build ./...`; CI uses Bazel (`bazel test --config=race //internal/kernel/...`).

## Why NO `level/` here?

`level` used to live at `internal/kernel/level/` but was moved to `internal/core/logger/level/` on 2026-04-19. `Debug / Info / Warn / Error` is logger-domain vocabulary — no non-logger domain will ever import it, so it fails the "generic" half of the kernel rule even though it's stdlib-only. See `.claude/contexts/sdk-layer-placement-audit.md`.

Lesson: stdlib-only is necessary but NOT sufficient. When considering a new kernel package, ask "would a future HTTP middleware, metrics writer, or cache reach for this?". If no, it belongs in `core/<domain>/`.

## Audit: who stays, who would leave

As of the 2026-04-19 audit (extended by ADR 0006 to admit `ring`):

- `errs` stays — every layer of the SDK uses typed errors, including kernel itself. Meta-infrastructure.
- `clock` stays — textbook generic time abstraction. Extended in place (2026-09) with the waiting half (`Waiter`/`Timer`/`Ticker`) plus `ManualClock`, which is still stdlib-only and still domain-free: no `Job`, no `Task`, no `Schedule` appears in a signature. `Clock` itself was deliberately NOT widened — it is reachable downstream through the `pkg/v1/cache.Config` alias, so a new method would break every consumer's hand-written double. See `clock/CLAUDE.md` §"Why `Clock` was NOT extended".
- `recycler` admitted by ADR 0010 — `Pool[T]` + `CappedPool[T]` are textbook generic object pools (reuse + reset + cap-discard). Any byte buffer, codec stream, HTTP body encoder, or metrics line writer can reuse the mechanism; the thresholds stay with the consumers.
- `buffer` stays — the `[]byte` pool is generic even though `service/logger` is the heaviest consumer today. Since ADR 0010 it is a thin specialisation over `recycler.CappedPool[*[]byte]` (the generic `Pool[T]` moved to `recycler`).
- `ring` admitted by ADR 0006 — SPSC bounded queue is a textbook generic primitive. Logger's async middleware is the only consumer today; metrics batchers and codec stream pipelines are obvious future users.
- `snapshot` admitted by ADR 0011 — `Value[T]` is a textbook generic copy-on-write container (lock-free `Load` + mutex-serialised writers). The codec registry consolidated its three hand-rolled `atomic.Pointer[map]` + CAS loops onto it; routing tables, feature-flag maps, and hot-reloaded config are obvious future consumers.
- `singleflight` admitted by ADR 0049 — `Group[K,V]` is textbook generic call deduplication (`Do`/`Forget`/`InFlight`; no domain word in any signature). It ships with **one** in-tree consumer, `internal/service/cache`, and that is recorded rather than dressed up: the second consumer claimed during planning (`config`) was checked and does not exist — `config.Load` is stateless and `pollWatcher.Watch` delegates the reload to a caller callback, so there is nothing to deduplicate. Two real candidates exist and were left alone (`service/validation.planFor`, `service/codec/tlv.typeInfoFor`, both `LoadOrStore` on a compile-once cache that accepts the duplicate in writing). The admission rests on the rule that governs — stdlib-only AND generic — and on the precedent in the row above it: **ADR 0025 admitted `cache` with ZERO domain consumers**, and it still has none besides `pkg/v1/cache`. A consumer count was never the bar; ADR 0010's "three copies already existed" was a *consolidation* argument, not a gate.

## Conventions unique to kernel

1. **Import-only-stdlib.** Kernel packages MAY import each other (e.g. `ring` imports `errs` for its sentinels) but MUST NOT reach into `core/*` or `service/*`. No package outside of `errs` may use `fmt.Errorf` / `errors.New` in production code.
2. **`errs` self-reference.** The `errs` package uses its own meta-codes 0.0.0.1..6 as documentary identifiers for Define-time validation failures — they are never instantiated as `*Error` sentinels.
3. **No domain vocabulary in public APIs.** No `User`, `Request`, `Log`, `Entity` in type names. `Queue[T]`, `Pool[T]`, `Clock`, `Error` all pass.
4. **IFACE-PLUGIN marker.** Constructors returning an interface backed by an unexported struct (`ring.New`) tag their interface with the `// IFACE-PLUGIN:` comment so ktn-linter recognises the swap-able-implementation pattern. **Exception:** `recycler` ships concrete structs (`Pool[T]` / `CappedPool[T]`) by deliberate choice (ADR 0010) — a single-impl interface there was over-abstraction. The `sync.Pool`-style names carry the recognized "Pool" role suffix, so they pass `KTN-STRUCT-ROLE` natively — no linter exemption needed.

## Do NOT

- Add a package here whose only consumer is a specific domain (logger, HTTP, DB). Put it under `internal/core/<domain>/` as a subpackage.
- Import anything from `internal/core/*` or `internal/service/*`; kernel sits at the bottom.
- Replace package-level singletons (`clock.System`, `errs` sentinels) in tests — inject at the call site instead. For time, `clock.NewManualClock(start)` is the canonical double; hand-rolling another `Now`/`Since` struct is churn.

## Verification

```
# Primary (Bazel — CI source of truth)
bazel test --config=race //internal/kernel/...

# Fallback (go test — quick local iteration)
cd internal/kernel
GOWORK=off go test -race -cover ./...
# expected: buffer 90%, clock 100%, errs 97.3%, ring 90%
```

## Subtree

- `errs/` — see `internal/kernel/errs/CLAUDE.md`
- `recycler/` — see `internal/kernel/recycler/CLAUDE.md`
- `snapshot/` — see `internal/kernel/snapshot/CLAUDE.md`
- `buffer/` — see `internal/kernel/buffer/CLAUDE.md`
- `clock/` — see `internal/kernel/clock/CLAUDE.md`
- `ring/` — see `internal/kernel/ring/CLAUDE.md`
- `batcher/` — see `internal/kernel/batcher/CLAUDE.md`
- `worker/` — see `internal/kernel/worker/CLAUDE.md`
- `cache/` — see `internal/kernel/cache/CLAUDE.md`
- `singleflight/` — see `internal/kernel/singleflight/CLAUDE.md` (call deduplication, ADR 0049 — and the ~2 µs threshold below which it costs more than it saves)
