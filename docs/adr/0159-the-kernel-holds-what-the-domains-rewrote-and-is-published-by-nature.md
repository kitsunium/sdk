# ADR 0159 — the kernel holds what the domains were rewriting, and is published by nature

- **Status**: Accepted
- **Date**: 2026-10-03
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0103](0103-a-bucket-per-caller-one-backoff-curve-and-a-retry-on-the-clock-it-is-given.md) (the backoff curve's home), [ADR 0071](0071-a-registry-refuses-what-it-cannot-store.md) (its "Why not: a generic registry primitive in the kernel" is reversed)
- **Related**: [ADR 0006](0006-sdk-error-code-registry-extension.md) (`ring`), [ADR 0010](0010-kernel-recycler-primitive.md) (`recycler`), [ADR 0011](0011-kernel-snapshot-primitive.md) (`snapshot`), [ADR 0014](0014-sdk-transform-crypto-ports-config-topology.md) (`batcher`), [ADR 0025](0025-sdk-cache-kernel.md) (`cache`), [ADR 0049](0049-cache-becomes-a-domain.md) (`singleflight`), [ADR 0052](0052-sdk-lock-domain.md) / [ADR 0073](0073-session-waits-are-abandonable.md) / [ADR 0081](0081-the-windows-file-lock-is-a-different-primitive.md) (the two `flock` copies), [ADR 0053](0053-sdk-events-domain.md) (`topic` declined), [ADR 0083](0083-a-path-is-a-chain-and-a-held-lock-can-lose-its-file.md) (`pathchain`), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md) (a kernel package published by alias), [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (the framework reaches only `pkg/v1`), [ADR 0155](0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md) (the families), [ADR 0156](0156-the-public-module-links-the-standard-library-and-nothing-else.md) (`semver`)

## Context

The kernel's rule is stdlib-only AND generic, and its packages pass it. What a
reading of the importers shows, on this tree, is that the services do not use
most of them, and the reasons are the kernel's:

- **Two packages have no importer at all.** `group` and `topic` are imported by
  their own tests and nothing else. `events` declined `topic` on purpose
  (ADR 0053) and nothing else wanted a broadcast.
- **The ones that exist are cut too narrow to adopt.** `group` returns the
  first error, while a probe or a consumer that runs N tasks needs all of them:
  `health`'s probe, `queue`'s consumer and `scheduler`'s run loop each run a
  bare `sync.WaitGroup` instead. `worker.Every` and `batcher` start a
  `time.NewTicker` of their own, so no service that waits on an injected clock
  can use them. `ring` is single-producer only.
- **The services carry what the kernel lacks.** The backoff curve ADR 0103
  published lives in `internal/service/resilience`, and five service domains
  (`net/server`, `mail/spool`, `statemachine`, `lifecycle`, `queue`) import
  the resilience engine for that curve alone. Six registries in the core
  (`codec`, `crypto`, `id`, `transform`, `view`, `writer`) are near-copies of
  one another, and `crypto` already has a private generic one. `lock` and
  `session` each implement the same non-blocking `flock`, and `lock` alone its
  `LockFileEx` twin.
  `golang.org/x/mod` stands in for a version comparison (ADR 0156).
- **The framework rewrites the rest.** It may import `pkg/v1` and
  `kernel/errs` only (ADR 0147), and `pkg/v1` publishes three kernel packages,
  `errs`, `clock` and `cache`. So `framework/` holds 17 hand-written copy-on-write
  `atomic.Pointer` values, a subscriber set, a `sync.WaitGroup` fan-out and a
  lock-free multi-producer ring of its own.

ADR 0071 rejected a generic registry because the registries "differ in key
type, in their error codes, and in extra indexes". The difference in codes is
real; it is also exactly what a constructor parameter carries.

## Decision

### 1. Removed: `topic`

`internal/kernel/topic` is deleted. Nothing imports it, the SDK's in-process
bus is `events` (ADR 0053), and a primitive kept for a consumer that may come
is a surface somebody maintains for nobody. The framework's observer set stays
its own until a second consumer shows it is generic.

### 2. Widened, then adopted: `group`, `worker`, `batcher`, `ring`

- `group` gains a join mode — wait for every task and return their errors
  joined — beside the first-error mode it has. `health`'s probe runner,
  `queue`'s consumer and `scheduler`'s run loop adopt it in place of their
  `sync.WaitGroup`s.
- `worker` and `batcher` take a clock (a `clock.Waiter` as built), so their
  ticks run on the clock a service injects; `clock.System` stays the default.
- `ring` gains a multi-producer mode beside the single-producer one.

### 3. Added: `backoff`, `semver`, `flock`, and a generic registry

| Primitive | Holds | Replaces |
|---|---|---|
| `backoff` | the ADR 0103 curve, unchanged — `Delay(n)`, its growth checked against the overflow ADR 0103 fixed | the curve inside `internal/service/resilience`, which re-exports it by alias so `resilience.Backoff` and `BackoffValue` keep their names |
| `semver` | SemVer 2.0.0 precedence with Go's `v`, and Go pseudo-versions (ADR 0156) | `golang.org/x/mod` |
| `flock` | a non-blocking try-lock and unlock over `flock(2)` and `LockFileEx`; never a blocking call — the domains poll it on their own clocks | the copies in `lock` and `session` |
| `plugin.Registry[K, V]` | a copy-on-write registry over `snapshot`: lookup, idempotent re-registration, `Unusable` before storing, and the caller's own codes for a conflict and an unusable value, passed at construction | the six near-copy registries in the core, which become instances; codec's MIME and extension indexes stay codec's |

### 4. Published by nature, as aliases

The primitives a program or the framework would otherwise rewrite are
published in `pkg/v1` as pure aliases of the kernel, grouped by what they are
(ADR 0155):

| `pkg/v1` path | Kernel packages |
|---|---|
| `concur/{group,singleflight,worker,batcher,snapshot,recycler}` | the concurrency primitives |
| `collections/{heap,ring}` | the data structures |
| `semver` | at the root, beside `errs` and `clock`: a value every family may compare and none owns |
| `app/resilience` | `backoff`, through the alias `resilience` already declares |

`pathchain`, `plugin`, `flock`, `buffer` and `cache` are not published on their
own: a program reaches them through the domain that uses them (`lock`, the
registries, the logger, the `cache` domain, which already publishes the
primitive beside itself). The kernel mirrors the two new families where they
apply — `kernel/concur`, `kernel/collections` — and keeps every other package
at its root.

### 5. A published primitive is frozen as any port is

An alias publishes the type, so a kernel interface published here is frozen at
publication (ADR 0039, the reason `clock.Clock` already is) and a concrete
shape changes only while v0 (ADR 0040). A primitive is widened BEFORE it is
published, which is why §2 comes first.

## Consequences / Semantics

- **Implemented by the reorganisation series**, kernel first: `topic`
  removed, the widenings and their adoptions, the four new primitives, the
  registries rewritten as instances, the five service edges onto `resilience`
  replaced by `kernel/backoff`, then the `pkg/v1` aliases. This record changes
  no code.
- **As implemented so far**, and where the code settled a detail this record
  left open:
  - `topic` is deleted, and the five service edges are gone: `lifecycle`,
    `queue`, `statemachine`, `mail/spool` and `net/server` import
    `kernel/backoff`, whose `Value.Delay` is the curve and whose `Grow`,
    `Widen`, `NormalMultiplier` and `NormalJitter` are its two halves as
    package functions, so nothing new reaches the public alias's method set.
  - `group`'s join mode is `NewJoined` — every failure joined in submission
    order, the first one still cancelling the siblings — beside `Collect`,
    which returns each task's value in order. `queue`'s consumer adopts
    `NewJoined` and `health`'s probe runner `Collect`. `scheduler` does not
    adopt it, deliberately: a job's error is published and never returned, and
    one entry's failure must not cancel the others.
  - `worker.Every` takes `WithClock` and `batcher.Config` a `Clock`, both a
    `clock.Waiter` (the narrowest port that arms a ticker), the wall clock when
    unset.
  - `plugin.Registry[K cmp.Ordered, V comparable]` landed with the metrics and
    trace exporter registries as its first instances. It REPORTS a conflict
    (`Publish` returns false) instead of taking the caller's codes at
    construction: the registrar asks `Unusable` before publishing and refuses
    with its own code, so the kernel table builds no error. The codec, writer,
    crypto, transform, id and view registries are still their own copies.
  - Not yet: `ring`'s multi-producer mode, `semver`, `flock`, and the `pkg/v1`
    aliases of §4.
- The framework replaces its copy-on-write values, its fan-out and its ring
  with the published ones as it is next touched; its observer set stays.
- Five service-to-service edges disappear from the dependency graph; the
  resilience engine stops being a hub for a curve.
- Each registry keeps its codes and reasons (`DUPLICATE_REGISTRATION`,
  `CODEC_NIL`, …): only the mechanism is shared, as ADR 0071 required of the
  guard.
- `pkg/v1`'s surface grows by nine packages, each an alias of a kernel package
  that already passed the kernel rule.

## Breaking changes

None for the published surface: the new `pkg/v1` packages are additions, and
`resilience.Backoff` keeps its name through the alias. Inside the SDK,
`internal/kernel/topic` disappears with no importer.

## Alternatives considered

- **Delete `group` too.** It has no importer for the reason it is cut too
  narrow, and three services hand-roll what a join mode gives them.
- **Keep the curve in `resilience` and let the domains import it.** Five
  edges from service to service for one function, and the framework's loops
  depend on a resilience engine they do not use.
- **Publish every kernel package.** `pathchain`, `plugin` and `flock` are
  measurements and guards a domain applies with a policy; published alone,
  they invite the policy-free use their own documents warn against.
- **A `kit` group name for the published primitives.** It collides with
  `framework/kit`; `concur` and `collections` say what the packages are.

## Deferred

- A keyed mutex and a semaphore — `statemachine`'s per-entity locks, the
  bulkhead, the hedge limiter and the stream-group limiter each hold one — wait
  for the first change that converts two of them.
- An agenda primitive for the three "sleep until the next one is due" loops
  (`scheduler`, `statemachine`, the lifecycle supervisor).

## References

- `go list` over the importers of every kernel package, on the tree this record
  was written against: `group` and `topic` imported by their own tests only.
- `internal/kernel/worker/every.go`, `internal/kernel/batcher/batcher.go` — the
  tickers that keep services from adopting them.
- `internal/core/crypto/registry_generic.go` — the generic registry that already
  existed, privately.
