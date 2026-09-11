<!-- generated from internal/core/vfs/path_bench_test.go — run `cd internal/core && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./vfs/` to refresh -->
# Benchmarks — `internal/core/vfs`

The two guards every call into either filesystem runs first: the path grammar
and the file-mode rule. Nothing here touches a device, so these numbers are a
floor under every number in `internal/service/vfs/BENCH.md` — whatever a write
costs, it costs this much before it starts.

The question they answer is not "is validation fast enough". It is **does the
accepting path allocate**, because these functions run on every `Open`, `Stat`,
`ReadDir`, `ReadFile`, `WriteFile`, `MkdirAll`, `Remove`, `RemoveAll` and
`WriteAtomic`, and an allocation there would be an allocation the caller never
asked for and cannot avoid.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU cores          | 8 (AMD EPYC 7351P 16-Core Processor) |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | `agent-a3061313ad370948a` |
| Git commit         | `e01714c` (pre-commit) |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
BenchmarkValidatePath/shallow-8              53504413    23.05 ns/op      0 B/op   0 allocs/op
BenchmarkValidatePath/deep-8                 13546720    81.59 ns/op      0 B/op   0 allocs/op
BenchmarkValidatePath/escaping-8              4695162   244.8  ns/op    208 B/op   2 allocs/op
BenchmarkValidateWritePath/shallow-8         47389524    25.89 ns/op      0 B/op   0 allocs/op
BenchmarkValidateWritePath/deep-8            14370138    82.33 ns/op      0 B/op   0 allocs/op
BenchmarkValidateWritePath/root-8             5420937   212.5  ns/op    208 B/op   2 allocs/op
BenchmarkValidatePerm/accepted-8            424013131     2.831 ns/op     0 B/op   0 allocs/op
BenchmarkValidatePerm/zero-8                  3919816   306.7  ns/op    224 B/op   3 allocs/op
BenchmarkValidatePerm/setuid-8                3979248   298.5  ns/op    224 B/op   3 allocs/op
```

## What the numbers say

**Every accepting path allocates nothing.** `ValidatePath` on the name shape
callers actually pass is 23 ns and 0 B; `ValidatePerm` on a legal mode is
2.8 ns and 0 B, because it is two integer comparisons and a branch. That is the
result the guards were written for: a filesystem call pays for validation in
nanoseconds and in no garbage at all.

**Refusing costs ~10× accepting, and allocates.** 208–224 B across 2–3
allocations, every time. That is the typed error being built — the `*errs.Error`
plus the log-only field carrying the offending path or mode. It is a deliberate
trade and it is on the *cold* path by construction: a program that refuses
enough paths for this to matter has a caller generating bad paths in a loop,
which is a different problem. **Do not** "optimise" it by hoisting the sentinel
and dropping the field: the field is the only place the offending value
survives, since ADR 0056 keeps paths out of the `Public` half.

**The write grammar is free.** `ValidateWritePath` is `ValidatePath` plus a
comparison against `"."`, and the numbers show exactly that: 25.89 ns against
23.05 ns shallow, 82.33 against 81.59 deep — the second pair is inside noise.
The root refusal that stops `RemoveAll(".")` from emptying a filesystem costs
about 2.8 ns on the accepting path.

**Depth costs what scanning the elements costs.** A 6-element path is 82 ns
against 23 ns for a 1-element one, linear in the number of separators, with no
allocation at either end. `fs.ValidPath` is doing the work and this package adds
nothing to it — which is the point of rule "one grammar, the stdlib's".
