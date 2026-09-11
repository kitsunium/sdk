<!-- generated from internal/service/cli/cli_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./cli/` to refresh -->
# Benchmarks — `internal/service/cli`

**The conclusion first, because it is the deliverable: nothing in this package
is worth optimising, and the numbers below are what says so rather than the
intuition that argument parsing "happens once".** "Once per process" is a
claim about frequency; it becomes an argument only when it is put beside what
a process costs, and it stops being one the moment somebody embeds the
executor in a REPL. Both cases are priced here.

Argument parsing is not a hot path. Measuring it anyway is what turns that
from an assumption into a fact, and it produced one genuinely useful result:
**76.8 % of what `New` costs is package `flag` binding flags** — work the
caller pays for at parse time regardless — which is why the one optimisation
this design invites is refused below with its price attached.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P 16-Core) |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | `jaimerias-que-tu-te-connect` |
| Git commit         | `0bfbf60` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run unless stated |

> **Machine load matters more here than anything the code does.** The suite was
> re-run on a busier box after a pure refactor (`slices.Clone` for an
> equivalent `append`, plus comments): **every allocation figure came back
> byte-identical** — 21, 24, 72, 72, 70, 622, 7 — while every ns/op rose by
> 1.5–1.6× (baseline 1746 → 2834, `New` 52.5 µs → 83.8 µs). The denominator
> moved with them: an empty Go process took 2.55 ms instead of 1.99 ms, so the
> ratio the conclusion rests on barely moved (2.9 % → 3.3 %).
>
> Read the allocation columns as the measurement and the ns columns as an
> order of magnitude. That is not a hedge — it is the finding: on a path that
> runs once per process, wall-clock is dominated by whatever else the machine
> is doing.

## Results

```
BenchmarkFlagParseBaseline-8        674888     1746 ns/op     1040 B/op     21 allocs/op
BenchmarkExecuteFlatCommand-8       591736     2043 ns/op     1080 B/op     24 allocs/op
BenchmarkExecuteNestedCommand-8     203526     5765 ns/op     3048 B/op     72 allocs/op
BenchmarkExecuteUnknownCommand-8    141116     9171 ns/op     3433 B/op     72 allocs/op
BenchmarkRenderHelp-8               131439     8517 ns/op     3161 B/op     70 allocs/op
BenchmarkNew-8                       22656    52545 ns/op    25968 B/op    622 allocs/op
BenchmarkFlagSource-8              1000000     1750 ns/op      712 B/op      7 allocs/op
```

`BenchmarkFlagParseBaseline` is package `flag` alone doing exactly the work
this domain delegates to it: build a `FlagSet`, bind five `int` flags, parse a
seven-token vector. Every other number is read against it. Without a control,
"resolving a sub-command costs 5.8 µs" is a number with no unit.

## What the domain adds to `flag` — the whole answer

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| `flag` alone, 5 flags | 1746 | 1040 | 21 |
| the same, through one `cli` leaf | 2043 | 1080 | **24** |
| **delta** | **+297** | **+40** | **+3** |

The nanosecond delta is inside the noise of the control and must not be quoted
as a precise figure: five repeats at `-benchtime=1s` gave the baseline
1803–2146 ns (±9 %) and the leaf 2387–2491 ns (±2 %), so the honest reading is
"somewhere between +240 ns and +690 ns". **The allocation delta does not
jitter: it was +40 B and +3 allocations on every run.** Those three are the
per-invocation slice of flag sets and the two clones that make
`InvocationValue.Path` and `.Args` safe to keep after `Execute` returns — the
cost of the domain's own promise that an `Action` may hold on to what it was
handed.

Read the other way: **package `flag` itself is 21 of the 24 allocations.** The
substrate dominates, which is the expected shape when a domain adds a tree and
a help renderer to a parser rather than replacing it.

## Depth costs one flag set, not one search

`BenchmarkExecuteNestedCommand` resolves `tool db schema migrate` through two
groups of nine children each, and then parses the same five flags:

```
flat    (1 set )   2043 ns    24 allocs
nested  (3 sets)   5765 ns    72 allocs      2.8× / 3.0×
```

Three flag sets cost three times one flag set. **Resolution itself is
invisible in the numbers** — the linear scan over nine sibling names and the
two extra path elements do not register beside `flag.NewFlagSet` plus five
`IntVar` calls. That is the measured justification for ADR 0065 §D3's "no
depth limit and no lookup map": a map keyed by name would replace a scan that
costs nothing with a map that costs an allocation per group, and would lose
the declaration order the help lists in.

## The failure path is the expensive one, and it is the rare one

```
RenderHelp             8517 ns    70 allocs
ExecuteUnknownCommand  9171 ns    72 allocs
```

Both render a full help page for a group of nine children that also declares
five flags. It is ~4× a successful nested invocation, and it happens at most
once per process — a run that reaches it does not go on to do the work the
tool exists for. It is also the case where the operator is about to read
several lines of text, so 9 µs is not the latency they will notice.

## `New`: 52 µs, once, and 77 % of it is `flag`'s

`BenchmarkNew` validates 1 root + 2 groups + 24 leaves and calls all 27
`Binder`s on throwaway sets. A memory profile at `alloc_objects`:

| site | share of `New`'s allocations |
|---|---|
| `validateFlags` (cumulative) | **76.85 %** |
| ├ the caller's own `Binder` → `flag.FlagSet.Int` / `.Var` | 65.33 % |
| └ `flag.NewFlagSet` (the throwaway probe set) | 11.52 % |
| `validate` itself (path `strings.Join`, the sibling-name map) | 13.42 % |
| `validateName` | 2.68 % |

The CPU profile agrees: `flag.(*FlagSet).Var` is 33.6 % cumulative, and the
top of the flat profile is `runtime.mallocgc`, `runtime.concatstrings` and the
map fast paths — string building and map insertion, which is what a validator
over a tree of names *is*.

**This domain's own share of `New` is ~13 %**, about 83 allocations across 27
commands: three per command, for the path segments and one map entry.

## The optimisation this design invites, priced and refused

Skipping the construction-time `Binder` probe would remove `validateFlags`
entirely: **76.8 % of `New`'s allocations and roughly 40 µs per process.**

It is refused, and the denominator is why. A Go binary that contains only an
empty `main` takes **1.99 ms** to start and exit (mean of 300 `fork`+`exec`
runs on this box, measured in the same conditions as the table above). Against
that:

| | µs | share of an empty process |
|---|---|---|
| `New` over a 27-command tree | 52.5 | 2.6 % |
| one nested `Execute` | 5.8 | 0.3 % |
| **the whole domain, once** | **58.3** | **2.9 %** |
| what the refused optimisation would save | 40 | 2.0 % |

Two percent of the time it takes to start a process that does nothing at all,
in exchange for the two properties the probe buys: the whole tree is validated
at construction rather than on the branch an invocation happens to take, and a
`Binder` that claims `-h` is refused in `main` instead of silently turning a
help page into a successful parse. That is not a trade; it is a discount
nobody asked for.

## The one case where "once per process" is false

An executor embedded in a REPL, a test harness or a long-lived supervisor runs
`Execute` repeatedly against one `New`. That is exactly why the two are
separate benchmarks: the 52 µs is paid once and the 2–6 µs per invocation is
what such a caller actually pays. At 6 µs, a REPL would need ~166 000 commands
per second before this domain appeared in a profile.

## `FlagSource`: 1.75 µs, 7 allocations

The config adapter over an invocation carrying three set flags across two
sets. It is the same order as one flag parse, it runs once per process beside
the `config.Load` it feeds, and its cost is dominated by the map it must
allocate — `Load` hands out a fresh copy on every call precisely because
`config`'s merge writes into the destination map it is given.

## What was NOT measured, and why

- **Concurrent `Execute`.** The engine holds no per-invocation state, so the
  number would be a function of the caller's `Action` and not of this package.
  The property that matters there is correctness, not throughput, and it is
  pinned by `TestTheEngineHoldsNothingPerInvocation` instead.
- **Help rendering as a function of tree width.** The renderer is one linear
  pass for the column width and one for the rows; there is no candidate
  quadratic to catch.
