<!-- generated from internal/service/validation/validation_bench_test.go — refresh with `cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./validation/` -->
# Benchmarks — `internal/service/validation`

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU               | AMD EPYC 7351P 16-Core, 8 vCPU visible |
| RAM               | 15 GiB |
| OS / kernel       | Linux 6.12.101+deb13-amd64 (Debian GNU/Linux 13, trixie) |
| Architecture      | amd64 |
| Go toolchain      | go1.27.1 linux/amd64 |
| Git branch        | `jaimerias-que-tu-te-connect` |
| Git commit        | `61d96c5` plus the JSON-reach check, measured on the tree that commits it |
| Generated (UTC)   | 2026-09-11 |
| Bench wall-clock  | `-benchtime=1s -count=10`, median quoted |

## What is being measured

ADR 0046 ships two front ends for the same rules. The programmatic one uses no
reflection — each descent is an accessor function the compiler can inline. The
struct-tag one buys ergonomics with `reflect`, and the SDK undertook to
**measure** that trade rather than assert it, and to **cache the plan per type**
so tag parsing never lands on a request path.

Both are measured on the same shape: a `benchUser` with three scalar rules and
a dived slice of three `benchAddress`, i.e. 3 field rules + 3 × 2 element
rules = 9 rule evaluations and 4 path descents per validation.

## Results

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `ProgrammaticValid` — reflection-free, value accepted | **716** | 96 | 6 |
| `ProgrammaticInvalid` — reflection-free, one violation | **970** | 416 | 11 |
| `TagValid` — compiled tag plan, value accepted | **935** | 144 | 7 |
| `TagInvalid` — compiled tag plan, one violation | **1 321** | 528 | 13 |
| `StructLookup` — `Struct[T]` on an already-compiled type | **92** | 24 | 1 |
| `PlanCompile` — one full tag walk of the type tree | **6 836** | 1 744 | 51 |

## How to read this

- **The plan cache is worth 74×.** Compiling the plan costs 6.8 µs; fetching it
  costs 92 ns. Without the cache, a validation of this shape would cost
  ~7.8 µs instead of ~1.0 µs — nearly eight times more, and it would put string
  splitting and `reflect.StructField` lookups on the hot path of every field of
  every request. That is the exact shape of the "we added validation and the
  p99 moved" post-mortem, and it is the reason `planCache` exists.

- **Reflection costs 31 %, not an order of magnitude.** 935 ns against 716 ns,
  and one extra allocation (48 B). The tag plan resolves every field index and
  every kind at compile time, so a validation performs no tag parsing, no name
  lookup and no type dispatch — what is left is `reflect.Value.Field(i)` plus
  the boxing of the value into a `reflect.Value` once per call. A caller who
  needs that 219 ns writes the accessors; everyone else takes the tags. Both
  produce the identical violation, which `TestTagAndCodePathsAgree` pins.

- **`Struct[T]` is cheap enough to call inside `Validate()`.** 92 ns and one
  allocation — the allocation is the `planKey` boxed into `sync.Map.Load`'s
  `any`. That is what makes the documented `config.Validator` bridge honest:
  the method can look the plan up on every call instead of the caller having to
  hoist a package-level var.

- **The accepting path allocates the PATHS, and nothing else.** Six allocations
  on `ProgrammaticValid` are six path strings: `addresses[0]`, `addresses[1]`,
  `addresses[2]` and the `.zip` extension of each. Every rule that accepts
  returns a nil `ReportValue`, so no report is ever built for a valid value —
  the 96 B is descent, not reporting.

  This is the measured cost of `Constraint` taking a `string` path. The
  alternative was considered and rejected: constraints could report at a
  *relative* path and each combinator could rewrite the prefix afterwards,
  which would move all path construction onto the failure path and take
  `ProgrammaticValid` to zero allocations. It would also make the `path`
  argument mean something different depending on where a constraint was
  composed, and oblige every hand-written constraint to return a freshly
  allocated report the caller may mutate. 96 bytes next to the JSON decode that
  produced the value is not worth turning a two-line port into a puzzle
  (ADR 0046 §Alternatives considered).

- **A violation costs ~390 ns and 6 allocations on the tag path, ~250 ns and 5
  on the programmatic one.** `TagInvalid` minus `TagValid`, and the same
  subtraction for the reflection-free pair, is the price of building one
  located violation and carrying it up through four levels of `append`. (This
  line used to quote the programmatic pair's ~280 ns under the tag pair's name.)
  It is charged only when something is wrong, which is the right way round: the
  accepting path is the one that runs on every valid request.

- **The compile now asks which field each JSON key reaches** (`checkJSONReach`,
  ADR 0046), so a rule on a field a shallower one hides, or that a tie at one
  depth leaves undecoded, is refused before it can judge a value no input sets.
  On this tree that is ~1.9 µs, 288 B and 12 of the compile's 51 allocations —
  measured by disabling it, which gave 4 985 ns, 1 456 B and 39. A type that
  embeds nothing and repeats no key skips the resolution entirely; without
  that shortcut the same check cost 6.1 µs and 22 allocations here. It runs
  once per type, like the rest of the compile.

## `singleflight` on `planFor`: measured, and refused

ADR 0049 shipped `kernel/singleflight` and named two candidates for it that it
had not checked. `planFor` is one of them. It is checked here, and the answer is
no.

**What the redundancy actually is.** `planFor` reads a `sync.Map`, and on a miss
compiles without holding anything, then publishes with `LoadOrStore`. So N
goroutines meeting a type for the first time simultaneously can each compile it,
and the first published plan wins. That is deliberate — the plans are equivalent,
so the redundancy costs CPU and never correctness — and it happens **once per
(type, stopAtFirst) per process**.

**The arithmetic**, from numbers measured on this machine:

| | ns | allocs |
|---|---:|---:|
| one plan compile (`PlanCompile`, above) | 6 836 | 51 |
| one `singleflight.Do`, uncontended (`internal/kernel/singleflight/BENCH.md`) | 2 032 | 6 |

For an N-way cold-start race on one type:

| | today | with `singleflight` |
|---|---|---|
| N = 2 | 13 672 ns of CPU | 6 836 + 2×2 032 = 10 900 ns |
| N = 8 | 54 688 ns of CPU | 6 836 + 8×2 032 = 23 092 ns |

So it does win arithmetically past two racers — by **32 µs, once per type**.

**And it is still refused**, because that is the whole prize. Against it:

- **six allocations and 2 µs added to every first sight**, including the
  overwhelmingly common one where nothing races at all and today's cost is a
  compile and a map store;
- **a goroutine per leading call** — `singleflight` runs `fn` on its own, which
  is what makes an abandoning caller not condemn the others. That machinery is
  the right answer for an origin fetch that takes milliseconds and can be
  cancelled. A 7 µs pure-CPU compile that no caller ever abandons needs none
  of it;
- **a new dependency edge** from `service/validation` to `kernel/singleflight`,
  to save 32 µs at boot;
- and the redundancy it removes is **already harmless**: two goroutines compile
  the same tags into two identical plans, and `LoadOrStore` publishes one.

A service registering two hundred validated types would save **6.3 ms at boot**,
and only if every one of those two hundred types were raced eight ways at the
same instant. That is the ceiling, not the expectation.

**What would reopen it**: a compile that grows expensive enough to matter — if
`compileStruct` ever reaches the hundreds of microseconds, the balance flips —
or a plan cache that has to be invalidated, which would turn a once-per-process
compile into a recurring one.

## Gates

- `TestTagAndCodePathsAgree` — the two front ends must produce the same
  violation, so a rule moved from a tag into code cannot silently change what a
  client sees.
- `TestThePlanCacheIsSafeAndStable` — sixteen goroutines compile-or-fetch the
  same plan under `-race` and must all see the same report.
- No `//go:build !race` file in this package, so nothing here needs an entry in
  `tools/alloc-lane-targets.txt` (CLAUDE.md rule 12). The allocation figures
  above are reported, not gated: they are a budget to watch, not an invariant —
  the invariant the domain actually rests on is that the accepting path builds
  no report at all.
