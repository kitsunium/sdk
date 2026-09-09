# internal/core/cache/

## Purpose

Declares the **cache domain**: the `Store[V]` port, the `EntryValue[V]` a caller
hands it, the `Fill[V]` func port, and the three siblings a store advertises by
type assertion (`EntryFetcher[V]`, `Tagger`, `Loader[V]`). A core sibling
admitted by **ADR 0049**, which amends ADR 0025. The concrete stores — memory
and chain — live in `internal/service/cache`; this package owns only the
contract, the domain value, and the typed sentinels.

Code range: `0.2.18.*` (ADR 0049).

## This is not `internal/kernel/cache`

`kernel/cache` is a **primitive**: a generic LRU + TTL map, no port, no error
surface, no vocabulary. It stays exactly what ADR 0025 made it, and the memory
store is built on it.

This package is the **domain**: an interface a caller holds without naming an
implementation, plus the three things a primitive deliberately does not have —
invalidation by tag, protection against a stampede of concurrent misses, and
tier chaining.

## Contents

| File | Surface |
|---|---|
| `cache.go` | the FROZEN `Store[V]` port (`Fetch`/`Set`/`Delete`), alone in the file that names the package |
| `cache_interface.go` | the ADR 0039 siblings — `EntryFetcher[V]` / `Tagger` / `Loader[V]` — plus the `Fill[V]` func port |
| `entry.go` | `EntryValue[V]` (Value / TTL / Tags) + `NoExpiry` |
| `codes.go` / `errors.go` | `0.2.18.*` — CACHE_MISCONFIGURED, CACHE_BACKEND_FAILED, CACHE_FILL_FAILED, CACHE_ENTRY_REJECTED |

## Conventions

- **`Fetch`, not `Get`, in the PORT too.** ADR 0025 named the primitive's read
  verb `Fetch` because a hit mutates (recency + counters). The port keeps the
  name so the honesty survives the layer boundary — and the memory store takes
  a **write** lock on `Fetch` for exactly that reason.
- **`Store` is FROZEN at three methods.** `pkg/v1/cache` aliases it, so under
  ADR 0039 a fourth method breaks every downstream implementer at compile time
  with no deprecation window. `TestThreeMethodDoubleStillSatisfiesStore` fails
  the build on anyone who folds a sibling back in.
- **`Fill` is a FUNC port**, like `resilience.Operation`, `scheduler.Job` and
  `validation.Constraint` — ADR 0039 satisfied structurally, because a func
  type cannot grow a method at all.
- **Keys are `string`, values are generic.** The primitive is generic in both;
  the domain narrows one half deliberately: a domain key appears in a tag
  bucket, an error field and a log line, and a generic `K` would need a
  formatting rule for each of those before it could.
- **A miss is not an error.** `Fetch` reports `(zero, false, nil)`. A cache
  that does not hold something has done nothing wrong.
- **Three TTL meanings, two spellings in the primitive.** `>0` is a lifetime,
  `0` means "the store's default", `NoExpiry` (−1) means no deadline. Any other
  negative value is refused, not reinterpreted — the primitive would read it as
  "no expiry", so accepting it stores an entry that never goes away.

## No registry — two reasons, and the second is decisive

The domain reason is `session`'s: the backend set is closed and small, and
picking one is a deployment decision made in code. Resolving it from a
configuration string would let a typo silently downgrade a shared cache to a
per-process one — and unlike a downgraded logger, a downgraded cache still
answers every call correctly, so nothing would ever surface.

The mechanical reason settles it on its own: `Store` is **generic in V**, and Go
has no `map[Name]Store[V]` for an open `V`. A registry would have to erase the
type and hand it back as `any`, which is precisely the unchecked cast the typed
port exists to remove.

(The "port + registry" criterion was retired: `proc`, `resilience`, `net`,
`scheduler`, `token`, `session` and `validation` all ship without one.)

## Do NOT

- Add a fourth method to `Store`. Add a sibling — the named test fails on this.
- Rename `Fetch` to `Get`. The LRU side effect makes a pure getter a lie, at
  every layer.
- Claim that `Loader` protects an origin from a fleet. It collapses concurrent
  misses **within one process**; N replicas still produce N fills. The port's
  own doc comment says so, and so does every document that mentions it.
- Put store bodies here — they live in `internal/service/cache`.

## Verification

```
cd internal/core && GOWORK=off go test -race ./cache/
bazel test --config=race //internal/core/cache:cache_test
```
