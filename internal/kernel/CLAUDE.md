<!-- updated: 2026-10-03T11:00:00Z -->
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
├── errs/  clock/  backoff/  semver/  plugin/  swar/  jsonenc/     the root: no family describes them
├── concur/        batcher, buffer, commit, group, pool, recycler, sema, singleflight, snapshot, worker
├── collections/   cache, heap, ring
└── fs/            flock, pathchain, seglog, winacl
```

### The root — what no family describes

| Package | Purpose | Code range |
|---|---|---|
| `errs/` | SDK-wide typed error + dotted-quad registry (ADR 0005) | `0.0.0.*` (meta-codes 0.0.0.1-6, documentary only) |
| `clock/` | time port, split in two: `Clock` (`Now` + `Since`) and `Waiter` (`After`/`NewTimer`/`NewTicker`/`Sleep`), joined by `Timed`; `System` delegates to package `time`, `ManualClock` is the deterministic test double | `0.1.2.*` (reserved — none emitted, none planned: bad input panics) |
| `backoff/` | `Value{BaseDelay, MaxDelay, Multiplier, Jitter}.Delay(attempt)` — the SDK's one exponential backoff curve, never negative (ADR 0103), plus its two halves `Grow` (pure) and `Widen` (jitter); `pkg/v1/app/resilience.Backoff` and `service/app/resilience.BackoffValue` alias it (ADR 0074) | (none — every input is normalised, nothing is refused) |
| `semver/` | SemVer 2.0.0 precedence with Go's leading `v`, and Go pseudo-versions: `IsValid` / `Compare` / `Prerelease` and `IsPseudoVersion` / `PseudoVersionRev` / `PseudoVersionTime` — the six functions the SDK called on `golang.org/x/mod`, which it replaces (ADR 0156 §4); one pass, no allocation, published as `pkg/v1/data/semver` | (none — answers with values and `bool`s; an invalid string orders below every version) |
| `plugin/` | `Unusable(v)` — the one question every process-wide registry asks before publishing: a typed nil and a non-comparable value both satisfy a port and neither can serve (ADR 0071) — and `Registry[K, V]`, the name-keyed copy-on-write table the entry is then published into, which reports a conflict (`Publish`) or the holder of a taken name (`Claim`) rather than building an error — every process-wide registry of the core is an instance (the metrics and trace exporters, codec, writer, crypto, transform, id, view) | (none — returns a reason string, and a conflict bool or a holder, the registrar puts behind its own code) |
| `jsonenc/` | the one JSON string escaper (`AppendString`, `AppendEscaped`: `"` `\`, the short escapes, `\u00xx`, U+2028/U+2029 escaped, invalid UTF-8 as U+FFFD, no HTML escaping), `AppendInt`/`AppendUint`, `AppendFloat` (encoding/json's float spelling: `1000000`, `1e-7`; NaN/±Inf refused with `ok == false`), and an ordered-key `Object` builder, over `[]byte`; the logger, OTLP, `redact` and token emitters write through it. Registers no codec; `codec/json.Marshal` is unchanged. Published through `service/data/codec/json` and `pkg/v1/data/codec/json` (ADR 0186 §D10, which amends ADR 0159) | (none — appends to the caller's slice) |
| `swar/` | first-match byte search a word at a time: `IndexByte2` (either of two bytes), `IndexLess` (a byte below a bound) and `Set.Index` (two bytes, a bound, and optionally the upper half of the alphabet) over `[]byte`, with exact lane tests, bounds-checked loads only and an AVX2 form behind `GOEXPERIMENT=simd` on amd64; the HTTP/1 codec's line ends and the JSON encoder's escapes, without a word of either in a signature. Not published (ADR 0186 §D10, which amends ADR 0159) | (none — an index or `-1`) |

### `concur/` — concurrency: goroutines run, joined and deduplicated, work coalesced, values shared or reused across goroutines (see `concur/CLAUDE.md`)

| Package | Purpose | Code range |
|---|---|---|
| `concur/batcher/` | generic `Batcher[T]` coalesce/flush/ticker buffer (ADR 0014); the `FlushEvery` ticker runs on `Config.Clock` (`clock.System` when nil); its own deliveries under a lifetime context `Close` cancels, `Close` bounded by its context, a capped batch delivered by the caller or — `FlushBackground` — by the batcher's goroutine (ADR 0216); published as `pkg/v1/concur/batcher` | `0.1.5.*` (BATCHER_CLOSED / BATCHER_DELIVER_FAILED / BATCHER_CLOSE_TIMED_OUT) |
| `concur/buffer/` | `sync.Pool` of `[]byte` in five size classes (1, 4, 16, 64, 256 KiB; `Get`, `GetSize`, `Put`) — `recycler.CappedPool[*[]byte]` specialisations, each keeping a buffer to twice its class (ADR 0216) | `0.1.1.*` (reserved) |
| `concur/commit/` | generic `Group[T]` group commit (#803, ADR 0207): concurrent `Do` calls each add an item, one goroutine runs the flush over the batch, every caller gets its verdict; batches gather for `MaxWait` on `Config.Clock` or up to `MaxItems`, flush one at a time in order; not published in `pkg/v1` | `0.1.4.*` (COMMIT_CLOSED) |
| `concur/group/` | generic structured concurrency (`Group`, `Go`/`Wait`/`Collect`, `Unlimited`); first error — or every error joined in submission order (`NewJoined`) — bounded parallelism, and a child panic delivered to the waiter; published as `pkg/v1/concur/group` | (none — forwards the task's error; a panic is re-raised, not coded) |
| `concur/pool/` | generic bounded worker pool (#804, ADR 0216): `Pool` from `NewPool(ctx, Config{Workers, Queue, OnPanic, OnError})`, `Submit` refusing when there is no room — never a park without a queue —, `Drain` / `Stop` joined within their context, a task's panic to `OnPanic`; published as `pkg/v1/concur/pool` | `0.1.6.*` (POOL_FULL / POOL_CLOSED) |
| `concur/recycler/` | generic `Pool[T]` + `CappedPool[T]` object pools (ADR 0010); published as `pkg/v1/concur/recycler` | (none — panics on programmer error) |
| `concur/sema/` | generic weighted semaphore (#812, ADR 0216): `Weighted`, FIFO and cancellable `Acquire(ctx, n)`, `TryAcquire`, `Release`; not published until a domain adopts it | `0.1.7.*` (WEIGHT_TOO_LARGE) |
| `concur/singleflight/` | generic `Group[K,V]` call deduplication (ADR 0049); one execution per key however many callers arrive; published as `pkg/v1/concur/singleflight` | (none — transparent to `fn`'s error; a panic is re-raised, not coded) |
| `concur/snapshot/` | generic `Value[T]` copy-on-write container (ADR 0011); published as `pkg/v1/concur/snapshot` | (none — never returns errors) |
| `concur/worker/` | generic goroutine-lifecycle daemon (`LoopDaemon`, `Start`/`Every`/`Stop`); `Every` ticks on an injectable clock (`WithClock`) and can end with its owner (`WithDone`); published as `pkg/v1/concur/worker` | (none — emits no codes) |

### `collections/` — generic containers: values kept in an order, within a bound, or within a bound and an age (see `collections/CLAUDE.md`)

| Package | Purpose | Code range |
|---|---|---|
| `collections/cache/` | generic `Cache[K,V]` LRU + TTL cache (ADR 0025); reuses `clock` for testable expiry | (none — `Fetch` returns `(V, bool)`) |
| `collections/heap/` | generic `Heap[T]` binary heap ordered by a caller-supplied comparison; published as `pkg/v1/collections/heap` | (none — `Pop`/`Peek` return `(T, bool)`; a nil comparison panics) |
| `collections/ring/` | SPSC lock-free bounded queue (ADR 0006), its cursors on their own cache lines, and its MPSC sibling `NewMulti` / `MultiQueue` (ADR 0159 §2, ADR 0216); published as `pkg/v1/collections/ring` | `0.1.3.*` (RING_FULL / RING_EMPTY / RING_CAP_ZERO emit today) |

### `fs/` — what the filesystem says, measured or called one primitive at a time, never decided (see `fs/CLAUDE.md`)

| Package | Purpose | Code range |
|---|---|---|
| `fs/flock/` | `TryLock(file)` / `Unlock(file)` / `Native` — an exclusive lock on a whole file that never blocks: `flock(LOCK_EX\|LOCK_NB)` on Linux and the BSDs, `LockFileEx` over every byte with `LOCKFILE_FAIL_IMMEDIATELY` on Windows (ADR 0081), `errors.ErrUnsupported` where neither exists; a caller polls on its own clock. The copy `service/app/lock` and `service/security/session` each carried (ADR 0159 §3) | (none — contention is `(false, nil)`, any other failure the kernel's errno, unwrapped) |
| `fs/pathchain/` | `Resolve(path)` — a path resolved one COMPONENT at a time over `os.Root` directory handles, reporting every indirection together with the mode of the directory that holds it. It is the measurement `O_NOFOLLOW` cannot give, since that flag governs the final component only (ADR 0083); it refuses nothing, because its callers — `service/app/lock`, `service/proc/ipc`, `service/security/session` — want different verdicts on the same shape | (none — returns the filesystem's own `*os.PathError`) |
| `fs/seglog/` | a durable segmented log of records addressed by a contiguous `uint64` index (#820, ADR 0230): `Open`, `Append` buffering, `Sync` under two syncs (body, then the end record), `Read`, `Range` (one `pread` per run), `Kind`, `DropBefore` (whole sealed segments, then a directory sync), `Rewrite` (a segment written again with records the caller changed, renamed over it), `Bounds`; recovery classifies every hole before a torn tail is zeroed, and refuses damage unchanged. Extracted from the consensus file log without term or hard state; not published | `0.1.8.*` (DAMAGED / VERSION_UNSUPPORTED / OUT_OF_SEQUENCE / DROPPED / OUT_OF_RANGE / FAILED / CLOSED / TOO_LARGE) |
| `fs/winacl/` | `GrantsAnyone(dir, onDirectory, onFilesWithin)` — reads a Windows directory's DACL and reports whether Everyone, Authenticated Users or BUILTIN\Users holds a right on the directory or on what the files created in it inherit, naming every answer that is not a verdict; plus the rights and the three masks (`ReplaceRights`, `CreateRights`, `ContentRights`) the SDK's directory rules ask (ADR 0084, ADR 0086, ADR 0095). Moved from `service/app/lock`, which exported it for `service/data/queue`. The reader is Windows-only; the vocabulary compiles everywhere | (none — a failed Win32 call travels as its status inside `observed`) |

Each package owns a sibling `CLAUDE.md` documenting its surface and contract.

## Module

A directory of the `github.com/kitsunium/sdk/internal` module (ADR 0171), whose `go.mod` is at `internal/` — a module of its own until ADR 0162, then a directory of the root module — with a shared test harness. Every package directory — at the root or under a family directory — is a Go package of that module (not a sub-module); a family directory (`concur/`, `collections/`, `fs/`) holds no Go code. Build with `cd internal/kernel && GOWORK=off go build ./...`; CI uses Bazel (`bazel test --config=race //internal/kernel/...`).

## Why NO `level/` here?

`level` used to live at `internal/kernel/level/` but was moved to `internal/core/observe/logger/level/` on 2026-04-19. `Debug / Info / Warn / Error` is logger-domain vocabulary — no non-logger domain will ever import it, so it fails the "generic" half of the kernel rule even though it's stdlib-only. See `.claude/contexts/sdk-layer-placement-audit.md`.

Lesson: stdlib-only is necessary but NOT sufficient. When considering a new kernel package, ask "would a future HTTP middleware, metrics writer, or cache reach for this?". If no, it belongs in `core/<domain>/`.

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
- `jsonenc/` — see `internal/kernel/jsonenc/CLAUDE.md` (the one JSON string escaper — what a string becomes, why invalid UTF-8 is repaired here and refused by the caller that must, and what is deliberately not migrated)
- `swar/` — see `internal/kernel/swar/CLAUDE.md` (first-match byte search — why the lane tests are exact, why there is no one-byte search, and what the AVX2 form owes the SWAR one)

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

`fs/` — see `internal/kernel/fs/CLAUDE.md` (the family: measurements of the filesystem and single primitives over it, never a verdict on them — and the five directory rules that share them and nothing else):

- `fs/flock/` — see `internal/kernel/fs/flock/CLAUDE.md` (a file lock that never blocks, over `flock(2)` and `LockFileEx` — the measured row where the two kernels are opposites, ADR 0081)
- `fs/pathchain/` — see `internal/kernel/fs/pathchain/CLAUDE.md` (a path resolved one component at a time — the measurement `O_NOFOLLOW` cannot give, ADR 0083)
- `fs/seglog/` — see `internal/kernel/fs/seglog/CLAUDE.md` (the segmented log extracted from consensus — the record, the two-sync batch, and the eight tail cases it is held to, ADR 0230)
- `fs/winacl/` — see `internal/kernel/fs/winacl/CLAUDE.md` (a Windows directory's DACL read for "can anybody?" — the reader's rules from ADR 0084/0086/0095, and its two callers' opposite answers to "could not look")

## Reference in NOTES.md

Moved unchanged to `internal/kernel/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `Audit: who stays, who would leave`.
