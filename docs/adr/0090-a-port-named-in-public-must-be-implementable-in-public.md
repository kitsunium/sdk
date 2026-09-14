# ADR 0090 — a port named in public must be implementable in public

- **Status**: Accepted
- **Date**: 2026-09-14
- **Deciders**: SDK maintainers
- **Related**: [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port grows by siblings, never by widening — the rule this publication makes binding on five more interfaces), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (a published concrete shape while v0), [ADR 0025](0025-sdk-cache-kernel.md) (the precedent: a kernel primitive published through `pkg/v1` aliases), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (what a public alias may point at), [ADR 0017](0017-pkg-bare-module-path.md) (the public module), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (the non-positive-period refusal this port carries)

## Context

`internal/kernel/clock` is the SDK's time port: `Clock` reads time, `Waiter`
waits for it, `Timed` is the union, `Timer` and `Ticker` are the handles a
`Waiter` returns, `System` is the production value and `ManualClock` is the
deterministic test double. It has never been published.

That reads like an ordinary "not yet promoted" primitive, and it is not. The
port was **already in the public API** — it just could not be used.

Every SDK configuration that takes a time source is a `pkg/v1` type alias onto
an internal struct whose `Clock` field is typed `clock.Clock` or `clock.Timed`.
So the names were in godoc, and the compiler printed one of them verbatim at a
downstream call site:

```
cannot use &myClock{} (value of type *myClock) as clock.Timed value in
struct literal: *myClock does not implement clock.Timed
(wrong type for method NewTicker)
        have NewTicker(time.Duration) *myTicker
        want NewTicker(time.Duration) clock.Ticker
```

`Waiter.NewTimer` returns `Timer` and `Waiter.NewTicker` returns `Ticker`. Go
compares method signatures by **type identity**, not by method set, and both
are declared behind the `internal/` firewall. Two escapes exist in principle
and both are closed; each was compiled rather than reasoned about:

| Attempt | Result |
|---|---|
| A locally declared `Ticker` interface with the identical method set | `have NewTicker(time.Duration) Ticker` / `want NewTicker(time.Duration) clock.Ticker` — two named interface types from different packages are never identical |
| `import "…/internal/kernel/clock"` from a consumer module | `use of internal package github.com/kitsunium/sdk/internal/kernel/clock not allowed` |

So `Timed` was a field every consumer could see, that no consumer could
satisfy, and whose only reachable value was nil — which the SDK reads as the
wall clock.

### The inventory, measured

Counted by resolving every `pkg/v1` alias to its internal type and then finding
every exported `clock`-typed field on the structs those aliases name. The
script is at the end of this ADR; run it rather than trusting the number.

| | Count |
|---|---|
| exported `clock`-typed fields in `internal/` | 25 |
| **published through a `pkg/v1` alias** | **23** |
| — typed `clock.Timed` (unimplementable from outside) | **9** |
| — typed `clock.Clock` (implementable, but unnameable) | **14** |
| not published | 2 (`service/trace.TracerConfig`, `service/writer/dbsink.Config`) |

The nine: `scheduler.Config`, `lock.MemoryConfig`, `lock.FileConfig`,
`lock.KeepaliveConfig`, `session.FileConfig`, `sql.Config`, `health.Config`,
`lifecycle.Config`, `queue.ConsumerConfig`. A hand-written list of these had
**eight** and missed `lock.MemoryConfig` — which is the argument for deriving
the number mechanically, and for the repository's standing rule that a counter
is measured and never deduced.

The fourteen `clock.Clock` fields are the softer half and were never broken:
Go's structural typing lets a downstream `struct { Now(); Since() }` be
assigned to them. What those fields lacked was a **name** for the type — so no
consumer could write `func withClock(c clock.Clock)`, declare a variable of
that type, or reach `clock.System` to state the production choice explicitly.

### What the absence cost

`ManualClock` is 13 KB of production code behind 20 KB of tests: an injectable
clock that moves only when a caller moves it, with `Advance`, `Set`, `Pending`
and a `BlockUntil` that closes the register-versus-advance race. It is exactly
the instrument a downstream test suite needs to assert a lease expiry, a cron
cadence, a retry backoff or a shutdown budget — and it was unreachable. The
nine domains above could be used downstream but not tested deterministically:
their only available clock was the wall clock, so a test of them had to sleep.

## Decision

### 1. Publish `internal/kernel/clock` through `pkg/v1/clock`, as aliases only

The new package is type aliases, one `var`, and one delegating constructor. No
behaviour, no wrapper, no defaulting, no new symbol. It follows ADR 0025's
precedent exactly: `pkg/v1/cache` publishes the kernel LRU primitive the same
way, and this is the same move for the same layer.

The surface is the kernel package **in full** — `Clock`, `Waiter`, `Timed`,
`Timer`, `Ticker`, `System`, `ManualClock`, `NewManualClock`. That is the
complete set of exported symbols in `internal/kernel/clock`; nothing is held
back and nothing is added. A facade that published a subset would recreate this
ADR's problem on whatever it left behind.

### 2. Aliases, never redeclarations — that is the whole mechanism

`type Ticker = kclock.Ticker` gives **type identity**. A redeclaration
(`type Ticker interface { … }`) would be a new named type, `*myClock` would
still not implement `kclock.Timed`, and the package would ship while fixing
nothing. This is not a stylistic preference here; it is the load-bearing part,
and the test pins it with a negative control — the same double with a locally
declared `Ticker` still fails to compile.

### 3. `Clock`, `Waiter`, `Timed`, `Timer` and `Ticker` are frozen

Under ADR 0039 a published interface is extended by a **sibling** interface
discovered by type assertion, never by widening. That rule now binds these
five, and the ADR is explicit that the commitment is not uniform:

- **`Clock` was already bindingly frozen.** It is two methods, satisfiable
  structurally, and `pkg/v1/cache.Config` has carried it into the released
  module since ADR 0025. Its own package comment already said so.
- **`Waiter`, `Timed`, `Timer` and `Ticker` become bindingly frozen now.** They
  were named in the public API but unimplementable, so no downstream double
  could exist and widening them broke nobody. After this change one can, and
  widening breaks it at compile time with no deprecation window. This is a real
  new commitment and it is taken deliberately rather than inherited quietly.

It is taken because the method sets are not speculative. `Clock` is `Now` +
`Since`. `Waiter` is `After` + `NewTimer` + `NewTicker` + `Sleep` — the
stdlib's own waiting surface. `Timer` and `Ticker` mirror `*time.Timer` and
`*time.Ticker`, whose method sets have been stable across every Go release
including the 1.23 timer rework, which changed semantics and not signatures.
The one plausible addition, an `AfterFunc`, has no consumer anywhere in this
tree and would land on a sibling if it acquired one.

### 4. `ManualClock` is published as a concrete type, and that is cheap here

ADR 0040 licenses a published concrete shape to change while the module is v0,
and prices the expensive case: a `*Value` shape the SDK **returns**, which a
caller destructures, so even an added field breaks a composite literal written
without field names.

`ManualClock` is not in that group. It has **zero exported fields** — `mu`,
`cond`, `now`, `waits`, `seq` are all unexported — so it can only be built
through `NewManualClock` and driven through its methods. A future field costs
no consumer anything. The v0 licence is therefore not spent here, which is
stated rather than left silent.

### 5. `System` stays a `var`, and the doc says it is a value and not a hook

The kernel declares `var System Timed = systemClock{}`; the facade mirrors it.
Re-exporting a package-level value as `var Name = internal.Name` is this
layer's existing convention — every `pkg/v1` error sentinel is spelled that way.

A `func System() Timed` was considered, because a `var` invites
`clock.System = fake` and that assignment misleads: it rebinds this package's
variable and changes nothing inside the SDK, which reads its own default
directly. The `var` is kept anyway, on two grounds. Turning it into a function
would be the facade choosing a different shape from the thing it re-exports,
which is the first step toward a facade with opinions — and the hazard is
identical inside the SDK today, so the facade would be papering over a kernel
property rather than reporting it. The doc comment states the property instead.

### 6. Nothing else is promoted

`worker`, `batcher`, `singleflight`, `ring`, `recycler`, `buffer`, `topic`,
`snapshot`, `heap`, `group`, `plugin` and `pathchain` stay internal. Each is a
separate decision with its own cost — a public package is an ADR, a
`CLAUDE.md`, a generated `README.md`, a `BUILD.bazel`, and an irreversible
freeze — and none of them has this one's property of already being named in the
public API.

## Consequences

- The nine `clock.Timed` configurations accept a downstream time source. The
  domains behind them — scheduler, lock, session, sql, health, lifecycle,
  queue — become deterministically testable by a consumer, which they were not.
- The fourteen `clock.Clock` fields gain a nameable type and a reachable
  `System`.
- `pkg/v1/clock` adds **no dependency**: the kernel package is stdlib-only, and
  `pkg` already requires `internal/kernel`.
- Five interfaces can no longer grow a method. See decision 3 for which half of
  that was already true.
- `ManualClock` joins the ADR 0040 inventory in
  `docs/pre-v1-published-shape-audit.md` as a published concrete shape — in the
  cheap group, having no exported field.
- **No error code and no `PP` range.** Nothing in this package can fail: it
  declares no function that returns an error, and the one constructor accepts
  every `time.Time`. A package that cannot fail does not get a code block, the
  same reasoning ADR 0062 applied.
- No behaviour changed anywhere. `internal/kernel/clock` is untouched by this
  change, and every existing caller resolves to the identical type.

## Breaking changes

None. `pkg/v1/clock` is a new package, `internal/kernel/clock` is untouched,
and every existing caller resolves to the identical type — an alias IS the
type it names. The commitment this change does make is forward-looking and is
decision 3: four interfaces that could not be implemented from outside become
implementable, and therefore frozen.

## Why not

- **Why not redeclare the interfaces in `pkg/v1` and adapt?** An adapter needs
  to convert a consumer's `Timer` into a `kclock.Timer`, which means wrapping
  every handle the clock hands out, on the hot path of every timeout in the
  SDK — to arrive at a type the alias gives for free and at zero cost. It would
  also be a second implementation of the port, in the layer least able to test
  it.
- **Why not move `clock` out of `internal/kernel` into a public package?** The
  kernel keeps its packages under rule 1 (stdlib-only AND generic), and `clock`
  passes both. Moving it would relocate a primitive to solve a publication
  problem that a facade already solves — and `pkg/v1/cache` set the precedent
  for the facade in ADR 0025.
- **Why not publish `Clock` alone, and leave the waiting half internal?** That
  is the current state, and it is what this ADR exists to end. `Timed` is the
  type nine configurations ask for; publishing only its reading half would
  leave every one of them exactly as unimplementable as before.
- **Why not add an `AfterFunc` while the port is being published?** Because it
  has no consumer. Adding a method on the way out of the door is how a frozen
  interface acquires a method nobody needs and everybody must implement; if one
  appears, ADR 0039 says it arrives as a sibling.
- **Why not a `Manual` alias with a shorter name?** The kernel type is
  `ManualClock` and the facade renames nothing. A facade that renames is a
  facade a reader has to translate, and the two names would then both appear in
  SDK-internal and consumer code for the same type.

## Verifying the inventory

```bash
python3 - <<'PY'
import re, io, glob, collections
alias = {}
for f in glob.glob('pkg/v1/**/*.go', recursive=True):
    if f.endswith('_test.go'): continue
    src = io.open(f, encoding='utf-8').read()
    imports = {}
    for im in re.finditer(r'^\s*(?:(\w+)\s+)?"(github\.com/kitsunium/sdk/internal/[\w/]+)"', src, re.M):
        p = im.group(2); imports[im.group(1) or p.rsplit('/', 1)[1]] = p
    facade = f[len('pkg/v1/'):].rsplit('/', 1)[0]
    for m in re.finditer(r'^type\s+([A-Z]\w*)(?:\[[^\]]*\])?\s*=\s*(\w+)\.([A-Z]\w*)', src, re.M):
        if (p := imports.get(m.group(2))):
            alias.setdefault((p, m.group(3)), set()).add(f"{facade}.{m.group(1)}")
rows = []
for f in glob.glob('internal/**/*.go', recursive=True):
    if f.endswith('_test.go'): continue
    pkgpath = 'github.com/kitsunium/sdk/' + f.rsplit('/', 1)[0]
    src = io.open(f, encoding='utf-8').read()
    # the type-parameter list must be optional: kernel/cache.Config is generic.
    for sm in re.finditer(r'^type\s+([A-Z]\w*)(?:\[[^\]]*\])?\s+struct\s*\{(.*?)^\}', src, re.M | re.S):
        for fm in re.finditer(r'^\s*([A-Z]\w*)\s+clock\.(Clock|Timed|Waiter)\s*$', sm.group(2), re.M):
            rows.append((sm.group(1), fm.group(2), sorted(alias.get((pkgpath, sm.group(1)), []))))
pub = [r for r in rows if r[2]]; k = collections.Counter(r[1] for r in pub)
print(f"exported clock fields: {len(rows)}   published: {len(pub)}   Timed={k['Timed']} Clock={k['Clock']}")
for kind in ('Timed', 'Clock'):
    print(kind, ":", ", ".join(sorted(a for r in pub if r[1] == kind for a in r[2])))
PY
```

## Deferred

- **An `AfterFunc` on `Waiter`.** Named in decision 3 and deferred with its
  reason: no consumer anywhere in this tree calls for one. If one appears it
  arrives as an ADR 0039 sibling, not as a fifth method.
- **The other twelve kernel primitives.** `worker`, `batcher`, `singleflight`,
  `ring`, `recycler`, `buffer`, `topic`, `snapshot`, `heap`, `group`, `plugin`
  and `pathchain` stay internal (decision 6). Each is its own decision with its
  own irreversible freeze; none has this one's property of already being named
  in the public API.
- **An in-repo consumer module for the acceptance test.** A1–A4 are verified
  from a module outside `github.com/kitsunium/sdk`, which is the only way to
  exercise the `internal/` firewall — but that module is built and thrown away
  rather than committed, because a new `go.mod` in this repository needs a
  `go.work` exclusion (Bazel's `go_deps` cannot process extra modules), a
  gazelle exclusion, and a named CI lane under rule 12. The in-repo
  `clock_external_test.go` covers the type-identity mechanism; the module
  boundary is Go's own rule.

## References

- Go specification, *Type identity* — two named interface types from different
  packages are distinct even with identical method sets; this is why an alias
  is required and a redeclaration does not work.
- Go, *internal packages* — `…/internal/x` is importable only from packages
  rooted at the parent of `internal`, which excludes every consumer module.
- `internal/kernel/clock/{clock,waiter,timer,ticker,system,manual}.go` — the
  published surface, unchanged by this ADR.
- `pkg/v1/clock/clock.go`, `pkg/v1/clock/clock_external_test.go` — the facade
  and the assertions behind decisions 2 and 3.
- `pkg/v1/cache/cache.go` — the ADR 0025 precedent this follows.
- `docs/pre-v1-published-shape-audit.md` — the ADR 0040 inventory
  `ManualClock` joins.
