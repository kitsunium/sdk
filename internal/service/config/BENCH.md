<!-- generated from internal/service/config/schema_bench_test.go — refresh with `cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./config/` -->
# Benchmarks — `internal/service/config`

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
| Git branch        | `agent-afbe1d8f07195288a` |
| Git commit        | `ea4888b` (the tree the schema was added to) |
| Generated (UTC)   | 2026-09-10 |
| Bench wall-clock  | `-benchtime=1s -count=5`, median quoted; the two `Load*` figures were re-taken at `-benchtime=500ms -count=9` because their difference is smaller than this box's noise band |

## What is being measured

Loading a configuration is a **start-up** path: it runs once per process. It is
measured anyway for two reasons. A `Watcher` REPLAYS it on every change, so a
cost that is invisible at boot is not automatically invisible in production.
And "compile once, check per load" is a claim rather than an observation, so
the compile half and the check half are timed separately instead of being
asserted to differ.

The shape is a realistic service configuration: twelve keys, one nested table,
`validate` tags on five of them, six declared defaults, one required key, and a
strict vocabulary (ADR 0061).

## Results

| Benchmark | ns/op | B/op | allocs/op | What it is |
|---|---:|---:|---:|---|
| `NewSchemaValue`         | 40 407 | 5 470 | 116 | compile: type walk, default layer round trip, tag plan, self-contradiction probe |
| `LoadWithoutSchema`      |  6 409 | 1 011 |  16 | the ADR 0028 baseline: merge + decode + `Validate` |
| `LoadSchema`             | 13 690 | 1 462 |  32 | the same load with defaults, key pass and constraints |
| `LoadSchemaAllowUnknown` | 12 508 | 1 390 |  28 | identical, with the unknown-key walk opted out |
| `CheckKeys`              |    884 |    72 |   4 | the key pass alone, over an already-merged map |
| `UnknownKeysWide`        | 21 125 | 4 464 |   8 | 100 unaddressable top-level keys — the FAILING path |
| `SchemaSourceLoad`       |  1 025 |   672 |   4 | the defensive copy `Schema.Source().Load()` hands out |

### The three numbers that matter

**A schema roughly doubles a load: 6.4 µs → 13.7 µs, 16 → 32 allocations.**
That buys the default layer, presence checking for the required keys, the
vocabulary check, and every `validate` tag on the type. It is paid once, at
start-up, in a process that is about to open sockets.

**The compile is 6× a load and happens once: 40.4 µs.** That is the whole
point of the split — the reflection walk over the target type, the JSON
normalisation of the default layer, the struct-tag plan, and the probe that
decodes the defaults alone to catch a schema contradicting itself all happen in
`NewSchemaValue`, and none of them happen again. A schema built per load would
put all 40 µs and 116 allocations on the `Watcher`'s path.

**The unknown-key check costs 1.2 µs and exactly 4 allocations.** `LoadSchema`
minus `LoadSchemaAllowUnknown` is 32 − 28 = 4 allocations and 72 B, which is
the same figure `CheckKeys` reports for the whole key pass — the presence
lookups themselves allocate nothing. Refusing a typo by default is not a
performance decision, and the measurement is here so nobody has to guess that
it might be.

## What the profiler said, and what was deliberately NOT done

`go test -bench=BenchmarkCheckKeys -memprofile` then `go tool pprof -top
-sample_index=alloc_objects`:

```
      flat  flat%   sum%        cum   cum%
  22937990 99.66% 99.66%   22937990 99.66%  …/service/config.joinKey (inline)
         0     0% 99.66%   22937990 99.66%  …/service/config.collectUnknown
```

**All four allocations of the key pass are `joinKey`**, building the dotted
path of a NESTED key before looking it up — a top-level key concatenates
nothing, so the four are exactly the four members of the one nested table. The
`make([]error, 0, keyFailureKinds)` in `rejectKeys` does not appear at all: it
never escapes on the accepting path.

They are removable. Walking with a reused `[]byte` scratch and looking up
`known[string(buf)]` uses the compiler's non-allocating map-index form, and
only materialises a string for a key actually being REPORTED. It is
deliberately not done: 884 ns and 72 B on a path that runs once per process do
not buy a buffer aliased across a recursion, which is the class of subtlety
that survives review and then breaks two years later.

The same profile on `BenchmarkLoadSchema` says where a load's cost really is:

```
   5046342 56.35% 56.35%  reflect.unsafe_New            ← encoding/json
   1343511 15.00% 71.35%  …/service/config.joinKey
    655370  7.32% 78.66%  …/core/validation.JoinField
    469756  5.25% 83.91%  …/service/config.cloneNested
```

**56 % of a load's allocations belong to the JSON round trip** the loader has
decoded through since ADR 0028, and `encoding/json` marshal + unmarshal is 24 %
of CPU samples. Removing every allocation the schema adds would leave the
dominant cost untouched. That is the reason the optimisation above is recorded
and refused rather than applied.

## The failing path is not the fast path

`UnknownKeysWide` — 100 unaddressable top-level keys, the shape an unprefixed
`EnvSource` produces — costs 21 µs and 4.5 KB. It is quoted so nobody
discovers it in an incident, not because it needs work: the run it happens on
is a run that ends in a refusal at start-up, and the eight allocations are the
growth of the report slice, not the walk. Every extra key is one map lookup
that misses.

## ADR 0097 — secret-aware loading and the provenance report

Measured on a different box from the table above, so compare these rows with
each other, not with it. This section has its own envelope:

| Dimension | Value |
|---|---|
| CPU               | Apple M1 Pro, 10 cores (8 performance + 2 efficiency), `GOMAXPROCS` 10 |
| RAM               | 16 GiB |
| OS / kernel       | macOS 26.6.2 (build 25G83), Darwin 25.6.0 |
| Architecture      | arm64 |
| Go toolchain      | go1.27.1 darwin/arm64 |
| Git commits       | before: `1c63300` (`main`, the base of the branch that added ADR 0097); after: `e03e72f` |
| Generated (UTC)   | 2026-09-25T16:44Z |
| Load              | a shared box: load average 4.7–5.5 on 10 cores during the run, which the ± column absorbs |
| Method            | both test binaries built first, then run INTERLEAVED — five rounds of `-test.count 2`, before then after — so a change in the box's load lands on both sides; `-benchtime=1s`; medians, ± and p-values from `benchstat` (Mann-Whitney U), n = 10 per cell |

```sh
# in a worktree of each commit, from internal/service:
GOWORK=off go test -c -o config.test ./config/
# then, five times, alternating the two binaries, from internal/service/config:
./config.test -test.run '^$' -test.bench '^Benchmark(LoadWithoutSchema|LoadSchema|LoadSchemaWithOrigins)$' -test.benchmem -test.count 2
benchstat before.txt after.txt
```

| Benchmark | before (`1c63300`) | after (`e03e72f`) | time, benchstat | What changed |
|---|---:|---:|---:|---|
| `LoadWithoutSchema` | 1.713 µs ± 1 % · 1 010 B · 16 allocs | 1.755 µs ± 2 % · 1 074 B · 17 allocs | +2.5 % (p = 0.000) | the layers slice a traced load reads; the secret walk is memoised per TYPE |
| `LoadSchema` | 3.705 µs ± 2 % · 1 460 B · 32 allocs | 3.730 µs ± 2 % · 1 524 B · 33 allocs | ~ (p = 0.225) | the same slice; a schema resolves its secret keys at construction |
| `LoadSchemaWithOrigins` | — | 5.122 µs ± 2 % · 2 872 B · 51 allocs | — | the report: one origin per leaf key, each attributed to its last layer |

A schemaless `Load` must know which keys hold a `secret.Value` to bypass the
environment's JSON coercion for them, and that is a reflection walk over the
target type. Walked per load, it DOUBLED the load — 3.1 µs and 34 allocations
on this box during development, a version of the code that no longer exists
and was therefore not re-measured — so the answer is memoised per type in a
`sync.Map`, the one piece of package state in the loader, holding one entry per
configuration type a program declares. What is left is one allocation and
64 B, the layers slice, and 2.5 % of a schemaless load. The report itself costs
1.4 µs and 18 allocations over `LoadSchema`, on a start-up path that runs once;
it is not paid by a caller who does not ask for origins.
