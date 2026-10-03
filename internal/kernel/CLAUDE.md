<!-- updated: 2026-10-03T03:20:00Z -->
# internal/kernel/

## Purpose

The SDK's lowest layer: **stdlib-only AND generic** primitives. A package qualifies for `kernel/` only if both halves are true — it imports nothing outside the Go standard library AND its vocabulary is domain-neutral enough that any future domain could plausibly import it. The 2026-04-19 layer audit (`.claude/contexts/sdk-layer-placement-audit.md`) is the canonical reference; it documents why `level` was relocated OUT and which packages remain.

## Contents

The kernel is grouped by FAMILY, the way every layer is (ADR 0155 §2): a
package that shares its kind with others sits under a family directory named
for that kind, and a package no family describes stays at the root. A family
directory holds no Go code — it is a prefix, not a package — and carries a
`CLAUDE.md` that lists its members and the rule that put them there.

```
internal/kernel/
├── errs/  clock/  backoff/  semver/  plugin/     the root: no family describes them
├── concur/        batcher, buffer, group, recycler, singleflight, snapshot, worker
├── collections/   cache, heap, ring
└── fs/            pathchain
```

### The root — what no family describes

| Package | Purpose | Code range |
|---|---|---|
| `errs/` | SDK-wide typed error + dotted-quad registry (ADR 0005) | `0.0.0.*` (meta-codes 0.0.0.1-6, documentary only) |
| `clock/` | time port, split in two: `Clock` (`Now` + `Since`) and `Waiter` (`After`/`NewTimer`/`NewTicker`/`Sleep`), joined by `Timed`; `System` delegates to package `time`, `ManualClock` is the deterministic test double | `0.1.2.*` (reserved — none emitted, none planned: bad input panics) |
| `backoff/` | `Value{BaseDelay, MaxDelay, Multiplier, Jitter}.Delay(attempt)` — the SDK's one exponential backoff curve, never negative (ADR 0103), plus its two halves `Grow` (pure) and `Widen` (jitter); `pkg/v1/resilience.Backoff` and `service/resilience.BackoffValue` alias it (ADR 0074) | (none — every input is normalised, nothing is refused) |
| `semver/` | SemVer 2.0.0 precedence with Go's leading `v`, and Go pseudo-versions: `IsValid` / `Compare` / `Prerelease` and `IsPseudoVersion` / `PseudoVersionRev` / `PseudoVersionTime` — the six functions the SDK called on `golang.org/x/mod`, which it replaces (ADR 0156 §4); one pass, no allocation, published as `pkg/v1/data/semver` | (none — answers with values and `bool`s; an invalid string orders below every version) |
| `plugin/` | `Unusable(v)` — the one question every process-wide registry asks before publishing: a typed nil and a non-comparable value both satisfy a port and neither can serve (ADR 0071) — and `Registry[K, V]`, the name-keyed copy-on-write table the entry is then published into, which reports a conflict rather than building an error (first users: the metrics and trace exporter registries) | (none — returns a reason string, and a conflict bool, the registrar puts behind its own code) |

### `concur/` — concurrency: goroutines run, joined and deduplicated, work coalesced, values shared or reused across goroutines (see `concur/CLAUDE.md`)

| Package | Purpose | Code range |
|---|---|---|
| `concur/batcher/` | generic `Batcher[T]` coalesce/flush/ticker buffer (ADR 0014); the `FlushEvery` ticker runs on `Config.Clock` (`clock.System` when nil) | `0.1.5.*` (BATCHER_CLOSED / BATCHER_DELIVER_FAILED) |
| `concur/buffer/` | `sync.Pool` of `[]byte` — a `recycler.CappedPool[*[]byte]` specialisation | `0.1.1.*` (reserved) |
| `concur/group/` | generic structured concurrency (`Group`, `Go`/`Wait`/`Collect`, `Unlimited`); first error — or every error joined in submission order (`NewJoined`) — bounded parallelism, and a child panic delivered to the waiter | (none — forwards the task's error; a panic is re-raised, not coded) |
| `concur/recycler/` | generic `Pool[T]` + `CappedPool[T]` object pools (ADR 0010) | (none — panics on programmer error) |
| `concur/singleflight/` | generic `Group[K,V]` call deduplication (ADR 0049); one execution per key however many callers arrive | (none — transparent to `fn`'s error; a panic is re-raised, not coded) |
| `concur/snapshot/` | generic `Value[T]` copy-on-write container (ADR 0011) | (none — never returns errors) |
| `concur/worker/` | generic goroutine-lifecycle daemon (`LoopDaemon`, `Start`/`Every`/`Stop`); `Every` ticks on an injectable clock (`WithClock`) and can end with its owner (`WithDone`) | (none — emits no codes) |

### `collections/` — generic containers: values kept in an order, within a bound, or within a bound and an age (see `collections/CLAUDE.md`)

| Package | Purpose | Code range |
|---|---|---|
| `collections/cache/` | generic `Cache[K,V]` LRU + TTL cache (ADR 0025); reuses `clock` for testable expiry | (none — `Fetch` returns `(V, bool)`) |
| `collections/heap/` | generic `Heap[T]` binary heap ordered by a caller-supplied comparison | (none — `Pop`/`Peek` return `(T, bool)`; a nil comparison panics) |
| `collections/ring/` | SPSC lock-free bounded queue (ADR 0006) | `0.1.3.*` (RING_FULL / RING_EMPTY / RING_CAP_ZERO emit today) |

### `fs/` — what the filesystem says about a path, measured rather than decided (see `fs/CLAUDE.md`)

| Package | Purpose | Code range |
|---|---|---|
| `fs/pathchain/` | `Resolve(path)` — a path resolved one COMPONENT at a time over `os.Root` directory handles, reporting every indirection together with the mode of the directory that holds it. It is the measurement `O_NOFOLLOW` cannot give, since that flag governs the final component only (ADR 0083); it refuses nothing, because the two callers in view want opposite verdicts on the same shape | (none — returns the filesystem's own `*os.PathError`) |

Each package owns a sibling `CLAUDE.md` documenting its surface and contract.

## Module

Single module `github.com/kitsunium/sdk/internal/kernel` — one `go.mod`, one `go.sum`, shared test harness. Every package directory — at the root or under a family directory — is a Go package of this one module (not a sub-module); a family directory (`concur/`, `collections/`, `fs/`) holds no Go code. Build with `cd internal/kernel && GOWORK=off go build ./...`; CI uses Bazel (`bazel test --config=race //internal/kernel/...`).

## Why NO `level/` here?

`level` used to live at `internal/kernel/level/` but was moved to `internal/core/observe/logger/level/` on 2026-04-19. `Debug / Info / Warn / Error` is logger-domain vocabulary — no non-logger domain will ever import it, so it fails the "generic" half of the kernel rule even though it's stdlib-only. See `.claude/contexts/sdk-layer-placement-audit.md`.

Lesson: stdlib-only is necessary but NOT sufficient. When considering a new kernel package, ask "would a future HTTP middleware, metrics writer, or cache reach for this?". If no, it belongs in `core/<domain>/`.

## Audit: who stays, who would leave

As of the 2026-04-19 audit (extended by ADR 0006 to admit `ring`):

- `errs` stays — every layer of the SDK uses typed errors, including kernel itself. Meta-infrastructure.
- `clock` stays — textbook generic time abstraction. Extended in place (2026-09) with the waiting half (`Waiter`/`Timer`/`Ticker`) plus `ManualClock`, which is still stdlib-only and still domain-free: no `Job`, no `Task`, no `Schedule` appears in a signature. `Clock` itself was deliberately NOT widened — it is reachable downstream through the `pkg/v1/data/cache.Config` alias, so a new method would break every consumer's hand-written double. See `clock/CLAUDE.md` §"Why `Clock` was NOT extended".
- `recycler` admitted by ADR 0010 — `Pool[T]` + `CappedPool[T]` are textbook generic object pools (reuse + reset + cap-discard). Any byte buffer, codec stream, HTTP body encoder, or metrics line writer can reuse the mechanism; the thresholds stay with the consumers.
- `buffer` stays — the `[]byte` pool is generic even though `service/observe/logger` is the heaviest consumer today. Since ADR 0010 it is a thin specialisation over `recycler.CappedPool[*[]byte]` (the generic `Pool[T]` moved to `recycler`).
- `ring` admitted by ADR 0006 — SPSC bounded queue is a textbook generic primitive. Logger's async middleware is the only consumer today; metrics batchers and codec stream pipelines are obvious future users.
- `snapshot` admitted by ADR 0011 — `Value[T]` is a textbook generic copy-on-write container (lock-free `Load` + mutex-serialised writers). The codec registry consolidated its three hand-rolled `atomic.Pointer[map]` + CAS loops onto it; routing tables, feature-flag maps, and hot-reloaded config are obvious future consumers.
- `singleflight` admitted by ADR 0049 — `Group[K,V]` is textbook generic call deduplication (`Do`/`Forget`/`InFlight`; no domain word in any signature). It ships with **one** in-tree consumer, `internal/service/data/cache`, and that is recorded rather than dressed up: the second consumer claimed during planning (`config`) was checked and does not exist — `config.Load` is stateless and `pollWatcher.Watch` delegates the reload to a caller callback, so there is nothing to deduplicate. Two real candidates exist and were left alone (`service/validation.planFor`, `service/data/codec/tlv.cachedStructTypeInfo`, both `LoadOrStore` on a compile-once cache that accepts the duplicate in writing). The admission rests on the rule that governs — stdlib-only AND generic — and on the precedent in the row above it: **ADR 0025 admitted `cache` with ZERO domain consumers**; today `internal/service/data/cache` and `internal/service/security/secret` build on it. A consumer count was never the bar; ADR 0010's "three copies already existed" was a *consolidation* argument, not a gate.
- `plugin` admitted by ADR 0071 — `Unusable(v any) (why string)` names no domain and knows no port; what it judges is the VALUE, and the two shapes it refuses are properties of Go's interfaces, not of any registry. It has **fifteen** in-tree consumers on day one, in eight core packages, which is unusual here and is the consolidation argument ADR 0010 made for `recycler`: the guard existed fifteen times as `if x == nil` and was wrong in the same way fifteen times. `Registry[K cmp.Ordered, V comparable]` joined it on the same argument: the name-keyed copy-on-write table behind a registry (a check-and-publish under `snapshot.Value`'s writer lock, idempotent on the identical value, a conflict REPORTED as a bool for the registrar to refuse with its own code) existed as a private copy per registry; it names no domain and builds no error, and its first users are the metrics and trace exporter registries.
- `backoff` admitted on rule 1 — stdlib-only, and its signatures name a
  duration, a factor, a ceiling and a jitter, nothing else — and moved here
  from `service/resilience`, where ADR 0103 had published it. It is the
  consolidation argument again: five service domains (`lifecycle`, `queue`,
  `statemachine`, `mail/spool`, `net/server`) imported the whole `resilience`
  package for this one type, which made a reliability-policy package the hub
  of the service graph. They import the kernel now, and the two aliases that
  keep the published names point at it (ADR 0074).
- `pathchain` admitted on rule 1: stdlib-only, and its signatures name a path
  and its components — no lock, no directory role, no policy. It shipped with
  one in-tree consumer, `internal/service/lock`, and that was stated rather than
  dressed up; `internal/service/proc/ipc` is the second (its socket directory's
  parents); three neighbours ask a related question with hand-rolled checks
  today (`internal/service/data/queue`'s symlinked state directory,
  `internal/core/data/vfs`'s `PathEscaped`, `internal/service/observe/logger/writer/rotfile`'s
  `refuseSymlink`) and none of them can ask THIS one. It deliberately produces
  no verdict: `/var/run` being a symbolic link is a distribution's decision and
  `/tmp/myapp` being one may be an attack, so the primitive measures and the
  domain decides.
- `semver` admitted on rule 1, and as a REPLACEMENT rather than a new
  capability: it takes the place of `golang.org/x/mod` (ADR 0156 §4), which
  was the one module outside the standard library the service layer required
  for something other than a codec. Its signatures take a version string and
  answer about it — no release, no update, no product — and its vocabulary is
  SemVer's and the Go toolchain's, which every Go program shares. It ships with
  one in-tree consumer, `internal/service/proc/self` (pseudo-versions); the two
  callers of `x/mod/semver`, `selfupdate` and `entitlement`, moved to the
  framework (ADR 0158) and reach it through `pkg/v1/data/semver`, the alias that
  exists for exactly that reason (ADR 0159 §4). Written from the
  specifications, not from x/mod's source; x/mod's test vectors are what the
  suite borrows, so the two are shown to agree.
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
  `service/data/queue` (through `NewJoined`, which was added for it), `heap` in
  `service/data/queue`'s lease expiry and `service/statemachine`'s agenda.
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

The root:

- `errs/` — see `internal/kernel/errs/CLAUDE.md`
- `clock/` — see `internal/kernel/clock/CLAUDE.md`
- `backoff/` — see `internal/kernel/backoff/CLAUDE.md` (the one backoff curve — why it is two halves, and the float-to-duration conversion it refuses to make first, ADR 0103)
- `semver/` — see `internal/kernel/semver/CLAUDE.md` (version precedence and pseudo-versions without `x/mod` — why the surface is six functions, and the oracles the suite holds it to)
- `plugin/` — see `internal/kernel/plugin/CLAUDE.md` (the registry entry guard and the table behind it — why both answer the registrar rather than refusing for it)

`concur/` — see `internal/kernel/concur/CLAUDE.md` (the family: what makes a primitive concurrent, and why `buffer` sits beside `recycler`):

- `concur/batcher/` — see `internal/kernel/concur/batcher/CLAUDE.md`
- `concur/buffer/` — see `internal/kernel/concur/buffer/CLAUDE.md`
- `concur/group/` — see `internal/kernel/concur/group/CLAUDE.md` (structured concurrency — what `Wait` guarantees, and why a task that ignores cancellation blocks it)
- `concur/recycler/` — see `internal/kernel/concur/recycler/CLAUDE.md`
- `concur/singleflight/` — see `internal/kernel/concur/singleflight/CLAUDE.md` (call deduplication, ADR 0049 — and the ~2 µs threshold below which it costs more than it saves)
- `concur/snapshot/` — see `internal/kernel/concur/snapshot/CLAUDE.md`
- `concur/worker/` — see `internal/kernel/concur/worker/CLAUDE.md`

`collections/` — see `internal/kernel/collections/CLAUDE.md` (the family: a container of values, generic in its element):

- `collections/cache/` — see `internal/kernel/collections/cache/CLAUDE.md`
- `collections/heap/` — see `internal/kernel/collections/heap/CLAUDE.md` (generic priority queue — and the measured cost of `container/heap`'s interface)
- `collections/ring/` — see `internal/kernel/collections/ring/CLAUDE.md`

`fs/` — see `internal/kernel/fs/CLAUDE.md` (the family: measurements of the filesystem, never a verdict on them):

- `fs/pathchain/` — see `internal/kernel/fs/pathchain/CLAUDE.md` (a path resolved one component at a time — the measurement `O_NOFOLLOW` cannot give, ADR 0083)
