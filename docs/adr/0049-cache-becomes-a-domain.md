# ADR 0049 — `cache` becomes a domain, and gains the kernel primitive it needed to

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0025](0025-sdk-cache-kernel.md) — which placed `Cache[K,V]` in the kernel and left it there. That placement stands; this ADR adds a layer above it rather than moving it.
- **Related**: [ADR 0010](0010-kernel-recycler-primitive.md) (kernel-primitive precedent), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (clamp or refuse), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows by siblings), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (a published shape may change while v0), [ADR 0045](0045-sdk-session-domain.md) / [ADR 0046](0046-sdk-validation-domain.md) (the two most recent core siblings, and the no-registry precedent)

## Context

ADR 0025 admitted `internal/kernel/cache` as a **primitive**: a generic LRU +
TTL map, stdlib only, no port, no error surface, no vocabulary. That was the
right call and nothing here reverses it.

What it left out is everything a cache needs once it stops being one
component's private detail:

- **Nothing else can invalidate what you cached.** The thing a caller wants to
  drop is almost never a key — it is "every entry derived from user 42", a set
  whose members are known to the writer and not to the invalidator. Without
  tags the invalidator's only options are to guess the key shape or to purge
  everything.
- **A hot key expiring is a load spike, not a miss.** Every request being
  served from that key misses at the same instant and every one of them calls
  the origin. The origin sees, in one moment, the traffic the cache was hiding
  — which is the load the cache was bought to prevent, arriving when the system
  is least able to absorb it.
- **There is no way to put one cache in front of another**, so a process-local
  tier and a shared tier cannot be composed at all.

Measured in this tree immediately before this change: `internal/kernel/cache`
had exactly **one** consumer, `pkg/v1/cache`, which aliases it. Nothing inside
the SDK reached for it in the fifteen months since ADR 0025. A primitive nobody
composes with is a primitive that has not yet been given the layer that would
make it useful — and after this change it has two, the second being the memory
store below.

## Decision

### 1. A kernel primitive first: `internal/kernel/singleflight`

`Group[K comparable, V any]` deduplicates concurrent calls on one key: N
callers, one execution, N results. Stdlib only, generic, no domain word in any
signature — the kernel gate (rule 1) is met on both halves.

**Two decisions inside it are the whole design.**

**A caller that abandons does not condemn the others.** The obvious
implementation runs `fn` on the first caller's goroutine with the first
caller's context, and it has a defect that appears only under load: the caller
who arrived first has been waiting longest and is therefore the one most likely
to give up first — and when it does, every other caller inherits its
cancellation. One abandoned request fails ten that were still willing to wait.

So `fn` runs on a goroutine of its own under
`context.WithCancel(context.WithoutCancel(leaderCtx))`, and that context is
cancelled only when the **last** caller leaves, tracked by a refcount. Three
consequences are named rather than left to be discovered: an abandoning caller
gets its own `ctx.Err()` and the call continues; nothing is computed for an
audience of zero; and the shared call carries the **first** caller's context
values and no caller's deadline, because one execution can carry only one set
of values.

**A panic is delivered, never swallowed.** `fn`'s panic is recovered *with the
stack of the goroutine that raised it* and re-raised in every waiter as a
`PanicValue`. Recovering into an `error` would convert a programming fault into
a value the caller may ignore and would lose the stack; letting it escape the
goroutine would take the process down while the waiters were still blocked on a
channel that will never close — and the crash dump would then point at N
goroutines parked in `await` rather than at the code that failed. One panic
becoming N is the honest arithmetic.

The price is measured, not assumed: **≈ 2 µs per leading call**, almost all of
it a goroutine park/unpark round trip (`BENCH.md` names it against a 0.7 ns
direct call). The rule that follows — a `Group` earns its place only when `fn`
costs materially more than that — is in the package documentation, because a
primitive whose overhead is not stated will be used where it makes things
slower.

### 2. The second-consumer claim, and what checking it found

ADR 0010 admitted `recycler` on a **consolidation** argument: three copies of
one mechanism already existed. The planning for this change asserted the same
shape here, naming `config` as a second consumer alongside the cache.

**That is false, and it is recorded rather than quietly dropped.**
`config.Load` is a stateless generic function with no cache and no dedup;
`pollWatcher.Watch` detects a change and invokes a caller-supplied `onChange`
callback. The SDK never reloads anything itself, so `config` has nothing to
deduplicate. Two other places do let concurrent goroutines duplicate work —
`internal/service/validation.planFor` and
`internal/service/codec/tlv.typeInfoFor`, both `sync.Map.LoadOrStore` on a
compile-once-per-type cache — but both accept the duplicate deliberately and in
writing, so they are **candidates, not consumers**. Converting them is a
behaviour change and belongs to its own commit.

`singleflight` therefore lands with **one** in-tree consumer. It is admitted on
the rule that actually governs, and on a precedent measured in this tree:

- Rule 1, the kernel gate, is *stdlib-only AND generic*. It says nothing about
  consumer counts, and `singleflight` passes both halves outright.
- **ADR 0025 itself admitted `cache` to the kernel with ZERO domain
  consumers** — a fact checked here rather than assumed, and one that remained
  true right up to this change. A consumer count was never the bar; ADR 0010's
  "three copies already existed" was a *consolidation* argument for a specific
  refactor, not a gate for every future primitive.

The design reason for keeping it separate from `cache` is independent of any
count: welding a dedup group into `internal/kernel/cache` would drag `context`
into a primitive that has none, would fuse two orthogonal mechanisms
permanently, and would leave the chain store — which needs deduplication
*without* an LRU behind it — with no way to reach it.

### 3. `cache` gains a core sibling and a service implementation

- `internal/core/cache` — `Store[V]` (`Fetch`/`Set`/`Delete`), `EntryValue[V]`,
  the `Fill[V]` func port, and three siblings: `EntryFetcher[V]`, `Tagger`,
  `Loader[V]`. Block `0.2.18.*`. The sibling named after its method is
  `Loader`, not `Filler`: the linter's `-er` rule is satisfied natively that
  way, and `EntryFetcher`/`Tagger` are the two exemptions — "FetchEntrier" and
  "InvalidateTagger" are not words, and both names describe the ROLE a store
  plays rather than its method, which is what makes a type assertion readable.
- `internal/service/cache` — `NewMemory` (LRU + tag index + stampede
  protection) and `NewChain` (L1/L2 with faithful promotion). Block
  `0.3.48.*`.
- `pkg/v1/cache` gains the domain **beside** the primitive it already
  publishes. `Cache[K,V]`, `Config`, `Stats` and `New` are untouched.

**The port is FROZEN at three methods**, and the guard is executable:
`TestThreeMethodDoubleStillSatisfiesStore` declares a bare three-method type
and assigns it to `Store[int]`. `pkg/v1/cache` aliases the interface, Go
interfaces are structural, and a fourth method would break every downstream
implementer at compile time with no deprecation window (ADR 0039). Everything
else is a sibling reached by type assertion — the model is codec's `Appender`.

**No shape published by ADR 0025 changed**, so the ADR 0040 v0 licence is not
used and is not needed. This is stated because ADR 0040 requires such a change
to be named out loud, and the honest report is that there was none.

### 4. `Fetch` stays `Fetch`, in the port too

ADR 0025 named the primitive's read verb `Fetch` because a hit mutates — LRU
promotion, counters. The port keeps the name so that honesty survives the layer
boundary, and the memory store makes it concrete: **`Fetch` takes a write
lock.** A read is not free and is not something to sprinkle through a function
that must not mutate.

### 5. Tag invalidation is indexed, and the cost is measured

The naive implementation walks the whole store and drops every entry whose tag
list contains the tag: O(N) in the **store** for an operation whose result is
usually a handful of entries, under the store's write lock.

Two maps instead: `byTag` (tag → keys) answers the question in one lookup;
`byKey` (key → its tags) exists only to make removal O(T) in that key's own
tags, and — more importantly — to keep `byTag` from growing forever when an
entry is evicted or expires.

The reverse index must be unlinked on **four** paths that do not share a
mechanism: capacity eviction and TTL expiry come back through `OnEvict`, an
overwrite is handled inside `tagIndex.add`, and `Delete`/`InvalidateTag` unlink
directly — because the primitive **deliberately does not fire `OnEvict` for an
explicit `Delete`**. A store relying only on the callback would leak every
deleted key's tags.

Measured (`internal/service/cache/BENCH.md`), removing 100 tagged entries:

| Store size | Time |
|---|---|
| 1 000 | 31.2 µs |
| 100 000 | 36.2 µs |

**A 100× larger store costs 1.16×.** A scanning implementation would cost
~100×. The residual 16 % is memory locality, not work. Tagging costs ≈ +760 ns
for the first tag and ≈ +390 ns per additional one, at ≈ 17 B/tag steady-state;
tag text is not duplicated between the maps, since Go strings are immutable.

### 6. One mutex, and `onEvict` never takes it

Both the primitive and the tag index sit under a single `sync.Mutex`, taken
exclusively even by `Fetch`. Nothing is lost — the primitive's own `Fetch`
already took a write lock — and everything is gained: index and entries agree
at every observable moment. With two locks a key would be briefly live but
unindexed, and an `InvalidateTag` crossing that window would report success
while leaving the entry fetchable.

The primitive invokes `OnEvict` after releasing its own lock but still on the
**caller's** goroutine — which is inside one of our methods, holding `s.mu`, and
`sync.Mutex` is not reentrant. So `onEvict` takes no lock: it appends to a
field reachable only while `s.mu` is held, and the surrounding method drains
it. `TestEvictionUnderTheStoreLockDoesNotDeadlock` provokes it — and if the
invariant breaks it **hangs** rather than failing, which is stated in the test
because a deadlock cannot be asserted against, only provoked.

### 7. Stampede protection stops at the process boundary — said four times

`Loader.Load` collapses concurrent misses on one key into a single fill
**within one process**. Ten replicas each running it still send ten concurrent
requests to the origin when a hot key expires.

That limit is written in the port's doc comment, in
`internal/service/cache/CLAUDE.md`, in the `pkg/v1/cache` package documentation
and here. Four places, deliberately, because a limit that appears in one place
is a limit most readers will miss — and a reader who takes "stampede
protection" to mean "my origin sees one request" will size an origin wrong by a
factor of N. Cross-process collapsing needs a shared lease, which is a
distributed system with its own failure modes (a holder that dies mid-fill, a
skew that lets two holders coexist), and this package does not have one.

A failed fill stores **nothing**. Caching a failure turns one bad minute at the
origin into a full TTL of confident wrong answers. A fill error already
carrying an SDK code propagates untouched (ADR 0005 §origin wins); an untyped
one is labelled `CACHE_FILL_FAILED`.

### 8. The chain is not atomic, and each ordering says which side it protects

Two independent stores, no transaction, so every multi-tier operation has a
window in which they disagree. The choice is which side of that window is
safer:

- **`Set`: far → near, stop on failure.** A near tier holding a value the far
  tier refused is a lie that outlives the error.
- **`Delete`: far → near, attempt EVERY tier.** Near-first is strictly worse: a
  failed far delete would let the next read miss near, hit far, and **promote
  the value back** — undoing the delete permanently. Far-first bounds the
  damage to one near-tier TTL, and the caller has an error.
- **`Fetch`: near → far; a broken tier stops the walk.** Continuing would serve
  a staler answer from behind it and call that a hit.
- **A failed promotion never fails the read.** The caller already has the
  value; failing would turn a degraded cache into a degraded service. It goes
  to `ChainConfig.OnPromoteError`, which is nil by default — and a nil hook is
  exactly how a near tier that rejects everything stays invisible, so the
  documentation says to wire it.
- **`InvalidateTag` counts REMOVALS, not distinct keys.** An entry in two tiers
  counts twice. Deduplicating would need each tier to return the keys it
  dropped, which no tier does; a number whose unit is stated beats one that is
  quietly approximate.

**Every tier must implement `EntryFetcher` and `Tagger`, checked once at
construction.** Promoting a value without its tags produces a copy
`InvalidateTag` can never reach again: the invalidation reports success, the far
tier forgets the entry, and the near tier keeps serving it until its TTL runs
out. A tag invalidation that silently misses one copy is worse than one that was
never offered, so the chain refuses to be built rather than degrade where nobody
is looking. `FetchEntry` also reports the **remaining** TTL, never the original
— otherwise a popular entry would be refreshed by every promotion and become
immortal.

### 9. No registry — and this time the mechanics settle it

The domain argument is `session`'s: the backend set is closed and small, and
picking one is a deployment decision made in code. Resolving it from a
configuration string would let a typo silently downgrade a shared cache to a
per-process one — and unlike a downgraded logger, a downgraded cache still
answers every call correctly, so nothing would ever surface.

The mechanical argument is decisive on its own: `Store` is **generic in V**, and
Go has no `map[Name]Store[V]` for an open `V`. A registry would have to erase
the type and hand it back as `any` — precisely the unchecked cast the typed
port exists to remove.

The "port + registry" criterion has been retired anyway: `proc`, `resilience`,
`net`, `scheduler`, `token`, `session` and `validation` all ship without one.

### 10. ADR 0031: this domain refuses, and clamps nothing

| Refused | Because |
|---|---|
| `MaxEntries <= 0` | a capacity is the whole statement of how much memory the caller will spend, so any number the SDK picked would be arbitrary — and the reading a zero would naturally get from the primitive, *unbounded*, is the dangerous one, doubly so because an unbounded store grows an unbounded tag index |
| `DefaultTTL < 0` | the primitive reads a negative TTL as "no expiry", so accepting it stores entries that never leave — the opposite of what was written |
| empty key, empty tag, negative per-entry TTL ≠ `NoExpiry` | each produces an entry the caller can never deliberately fetch, invalidate or replace |
| a chain of < 2 tiers, a nil tier, an incomplete tier | see §8 |

Nothing is clamped: there is no knob here whose floor the SDK can supply
without inventing the caller's requirement.

`NoExpiry` (−1) is a **third** TTL meaning on purpose. `>0` is a lifetime, `0`
is "the store's default", `NoExpiry` is "no deadline". A store with a
one-minute default holding one entry that must never expire is a real
combination, and reading both meanings out of `0` would make the second one
unsayable — the caller would have to know the default and restate it, which
stops being true the day the default changes.

## Consequences

- **A 16th core sibling.** `internal/core/CLAUDE.md`, the root `CLAUDE.md`
  domain list, `internal/service/CLAUDE.md` and `pkg/v1/CLAUDE.md` all gain a
  row (rule 11). `internal/kernel/CLAUDE.md` gains a tenth kernel package.
- Two new blocks in `codeRangeOwners`: `0.2.18.*` → `internal/core/cache` and
  `0.3.48.*` → `internal/service/cache` (table keys `0x00_02_12_00` and
  `0x00_03_30_00`), plus their `audit_srcs`.
  `docs/error-codes.yaml` regenerated.
- `pkg/v1/cache` now depends on `internal/core/cache` and
  `internal/service/cache` as well as the kernel primitive. All three are
  stdlib-only, so the package stays dep-light.
- **A key reaches the log.** Error *fields* carry the cache key. Fields are
  log-only and never rendered by `Error()` (ADR 0005 §4), but a caller who puts
  a secret in a cache key has put it in their logs, and the package
  documentation says so.
- `singleflight` ships with one consumer and a written record of why the second
  one that was claimed does not exist. Two candidates are named; converting
  them is a separate change.

## Deferred, by name

- **Redis, memcached, and every distributed backend.** Exception 4 of the
  third-party doctrine: they belong under `third-party/`, with their own
  dependency budget, in their own change. The chain is the seam they will
  attach to, and `EntryFetcher` is the method they will have to implement.
- **Cross-process stampede protection.** Needs a shared lease. See §7.
- **A `Stats` sibling on the port.** The primitive counts hits/misses/evictions
  and the domain does not expose them yet. It is a sibling interface when it
  lands, not a fourth method.
- **An active TTL sweeper.** Expiry stays lazy, as ADR 0025 left it. A store
  that ran its own goroutine would own a cadence the caller never asked for —
  the same argument `session.Sweeper` makes.
- **Converting `validation.planFor` and `codec/tlv.typeInfoFor` to
  `singleflight`.** Both currently accept duplicate concurrent compilation in
  writing; changing that is a behaviour change with its own measurement.

## Why not

- **Fold `singleflight` into `internal/kernel/cache`.** Rejected: it would drag
  `context` into a primitive that has none, fuse two orthogonal mechanisms
  permanently, and leave the chain store — which needs deduplication without an
  LRU behind it — unable to reach it.
- **Move `Cache[K,V]` out of the kernel into the new domain.** Rejected: ADR
  0025's placement is right, `pkg/v1/cache` publishes it, and a generic LRU map
  really is domain-neutral. The domain is added *above* it.
- **Widen `Store` with `InvalidateTag` and `Load`.** Rejected by ADR 0039: the
  alias publishes the interface and Go interfaces are structural, so every
  downstream three-method implementation would break at compile time with no
  deprecation window. The named test enforces it.
- **Let a chain tier get away without `EntryFetcher`.** Rejected: promotion
  would silently drop tags, and the resulting invalidation miss is invisible
  until an incident. Refusing at construction is one error message; the
  alternative is a bug with no symptom.
- **Fail the read when a promotion fails.** Rejected: the caller already has
  the value. Reporting through a hook keeps the failure visible without turning
  a degraded cache into a degraded service.
- **Return an error instead of re-panicking when `fn` panics.** Rejected: it
  converts a programming fault into a value a caller may ignore and discards
  the originating stack, which is the only thing that still points at the code
  that failed once the panic has crossed a goroutine boundary.
- **Default `MaxEntries` to some conventional number.** Rejected under
  ADR 0031: it is the caller's requirement, not a floor the SDK can supply.

## References

- Impl: `internal/kernel/singleflight/`, `internal/core/cache/`,
  `internal/service/cache/`, `pkg/v1/cache/`.
- Measurements: `internal/kernel/singleflight/BENCH.md` (the ~2 µs threshold),
  `internal/service/cache/BENCH.md` (invalidation at two store sizes).
- [ADR 0025](0025-sdk-cache-kernel.md) (amended), [ADR 0031](0031-policy-zero-values-are-never-inert.md), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md), [ADR 0040](0040-changing-a-published-shape-while-v0.md), [ADR 0045](0045-sdk-session-domain.md), [ADR 0046](0046-sdk-validation-domain.md).
