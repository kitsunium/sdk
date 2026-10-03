<!-- updated: 2026-09-28T19:19:15Z -->
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
| `batcher/` | generic `Batcher[T]` coalesce/flush/ticker buffer (ADR 0014); the `FlushEvery` ticker runs on `Config.Clock` (`clock.System` when nil) | `0.1.5.*` (BATCHER_CLOSED / BATCHER_DELIVER_FAILED) |
| `backoff/` | `Value{BaseDelay, MaxDelay, Multiplier, Jitter}.Delay(attempt)` — the SDK's one exponential backoff curve, never negative (ADR 0103), plus its two halves `Grow` (pure) and `Widen` (jitter); `pkg/v1/resilience.Backoff` and `service/resilience.BackoffValue` alias it (ADR 0074) | (none — every input is normalised, nothing is refused) |
| `worker/` | generic goroutine-lifecycle daemon (`LoopDaemon`, `Start`/`Every`/`Stop`); `Every` ticks on an injectable clock (`WithClock`) and can end with its owner (`WithDone`) | (none — emits no codes) |
| `cache/` | generic `Cache[K,V]` LRU + TTL cache (ADR 0025); reuses `clock` for testable expiry | (none — `Fetch` returns `(V, bool)`) |
| `singleflight/` | generic `Group[K,V]` call deduplication (ADR 0049); one execution per key however many callers arrive | (none — transparent to `fn`'s error; a panic is re-raised, not coded) |
| `group/` | generic structured concurrency (`Group`, `Go`/`Wait`/`Collect`, `Unlimited`); first error — or every error joined in submission order (`NewJoined`) — bounded parallelism, and a child panic delivered to the waiter | (none — forwards the task's error; a panic is re-raised, not coded) |
| `heap/` | generic `Heap[T]` binary heap ordered by a caller-supplied comparison | (none — `Pop`/`Peek` return `(T, bool)`; a nil comparison panics) |
| `pathchain/` | `Resolve(path)` — a path resolved one COMPONENT at a time over `os.Root` directory handles, reporting every indirection together with the mode of the directory that holds it. It is the measurement `O_NOFOLLOW` cannot give, since that flag governs the final component only (ADR 0083); it refuses nothing, because the two callers in view want opposite verdicts on the same shape | (none — returns the filesystem's own `*os.PathError`) |
| `plugin/` | `Unusable(v)` — the one question every process-wide registry asks before publishing: a typed nil and a non-comparable value both satisfy a port and neither can serve (ADR 0071) | (none — returns a reason string the registrar puts behind its own code) |

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
- `singleflight` admitted by ADR 0049 — `Group[K,V]` is textbook generic call deduplication (`Do`/`Forget`/`InFlight`; no domain word in any signature). It ships with **one** in-tree consumer, `internal/service/cache`, and that is recorded rather than dressed up: the second consumer claimed during planning (`config`) was checked and does not exist — `config.Load` is stateless and `pollWatcher.Watch` delegates the reload to a caller callback, so there is nothing to deduplicate. Two real candidates exist and were left alone (`service/validation.planFor`, `service/codec/tlv.cachedStructTypeInfo`, both `LoadOrStore` on a compile-once cache that accepts the duplicate in writing). The admission rests on the rule that governs — stdlib-only AND generic — and on the precedent in the row above it: **ADR 0025 admitted `cache` with ZERO domain consumers**; today `internal/service/cache` and `internal/service/secret` build on it. A consumer count was never the bar; ADR 0010's "three copies already existed" was a *consolidation* argument, not a gate.
- `plugin` admitted by ADR 0071 — `Unusable(v any) (why string)` names no domain and knows no port; what it judges is the VALUE, and the two shapes it refuses are properties of Go's interfaces, not of any registry. It has **fifteen** in-tree consumers on day one, in eight core packages, which is unusual here and is the consolidation argument ADR 0010 made for `recycler`: the guard existed fifteen times as `if x == nil` and was wrong in the same way fifteen times.
- `backoff` admitted on rule 1 — stdlib-only, and its signatures name a
  duration, a factor, a ceiling and a jitter, nothing else — and moved here
  from `service/resilience`, where ADR 0103 had published it. It is the
  consolidation argument again: five service domains (`lifecycle`, `queue`,
  `statemachine`, `mail/spool`, `net/server`) imported the whole `resilience`
  package for this one type, which made a reliability-policy package the hub
  of the service graph. They import the kernel now, and the two aliases that
  keep the published names point at it (ADR 0074).
- `pathchain` admitted on rule 1: stdlib-only, and its signatures name a path
  and its components — no lock, no directory role, no policy. It ships with one
  in-tree consumer, `internal/service/lock`, and that is stated rather than
  dressed up; three neighbours ask a related question with hand-rolled checks
  today (`internal/service/queue`'s symlinked state directory,
  `internal/core/vfs`'s `PathEscaped`, `internal/service/writer/rotfile`'s
  `refuseSymlink`) and none of them can ask THIS one. It deliberately produces
  no verdict: `/var/run` being a symbolic link is a distribution's decision and
  `/tmp/myapp` being one may be an attack, so the primitive measures and the
  domain decides.
- `group` and `heap` admitted on **rule 1 alone**, which is the only
  admission criterion there has ever been: stdlib-only AND generic. Each is
  domain-neutral down to its signatures — `Go`/`Wait`/`Collect` with no `Job` or
  `Worker`; a `T` and a `func(a, b T) int` with no `Priority`. **No consumer
  count was required.** The "≥2 concrete consumers" line sometimes attributed
  to ADR 0010 is not a kernel rule: it sits in that ADR's *Deferred* section and
  concerns exactly one primitive (`worker`). The governing precedent is the row
  above it — **ADR 0025 admitted `cache` with ZERO consumers** — and ADR 0010's
  "three copies already existed" was a *consolidation* argument, not a gate.
  Both have consumers now: `group` in `service/health` (through `Collect`) and
  `service/queue` (through `NewJoined`, which was added for it), `heap` in
  `service/queue`'s lease expiry and `service/statemachine`'s agenda.
- `topic` — a typed in-process broadcast admitted on the same rule — was
  **deleted** (2026-10). Its only candidate consumer, `service/events`, read it
  and refused it in writing (ADR 0053 §D9: a per-subscriber buffer makes
  `Publish` return before the listeners have run and puts `Halt` on the wrong
  side of a channel), and nothing else ever imported it. Admission needs no
  consumer count; keeping a primitive nobody can use still costs a package, a
  `CLAUDE.md`, a `BENCH.md`, linter exemptions and a reader's attention. If a
  fan-out to independent readers is ever needed, the history holds it.

## Conventions unique to kernel

1. **Import-only-stdlib.** Kernel packages MAY import each other (e.g. `ring` imports `errs` for its sentinels) but MUST NOT reach into `core/*` or `service/*`. No package — `errs` included — may use `fmt.Errorf` / `errors.New` in production code; `make guard` (sdkguard SDK002) fails `make lint` and CI on one.
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
# expected: buffer 100%, clock 100%, errs 99.7%, ring 100%
```

## Subtree

- `errs/` — see `internal/kernel/errs/CLAUDE.md`
- `recycler/` — see `internal/kernel/recycler/CLAUDE.md`
- `snapshot/` — see `internal/kernel/snapshot/CLAUDE.md`
- `buffer/` — see `internal/kernel/buffer/CLAUDE.md`
- `clock/` — see `internal/kernel/clock/CLAUDE.md`
- `ring/` — see `internal/kernel/ring/CLAUDE.md`
- `batcher/` — see `internal/kernel/batcher/CLAUDE.md`
- `backoff/` — see `internal/kernel/backoff/CLAUDE.md` (the one backoff curve — why it is two halves, and the float-to-duration conversion it refuses to make first, ADR 0103)
- `worker/` — see `internal/kernel/worker/CLAUDE.md`
- `cache/` — see `internal/kernel/cache/CLAUDE.md`
- `singleflight/` — see `internal/kernel/singleflight/CLAUDE.md` (call deduplication, ADR 0049 — and the ~2 µs threshold below which it costs more than it saves)
- `group/` — see `internal/kernel/group/CLAUDE.md` (structured concurrency — what `Wait` guarantees, and why a task that ignores cancellation blocks it)
- `heap/` — see `internal/kernel/heap/CLAUDE.md` (generic priority queue — and the measured cost of `container/heap`'s interface)
- `plugin/` — see `internal/kernel/plugin/CLAUDE.md` (the registry entry guard — why it returns a reason string and not an error)
- `pathchain/` — see `internal/kernel/pathchain/CLAUDE.md` (a path resolved one component at a time — the measurement `O_NOFOLLOW` cannot give, ADR 0083)
