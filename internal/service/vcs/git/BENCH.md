<!-- generated from internal/service/vcs/git/diff_parse_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./vcs/git/` to refresh -->
# Benchmarks — `internal/service/vcs/git`

**"I want to ask `ContainsLine` once per diagnostic. Can I afford that?"**

**Yes, and by three orders of magnitude.** Resolving a busy branch costs
**≈155 µs**, once. Asking that resolution a question costs **≈49 ns**, and
allocates **nothing**. The ratio is **≈3 180×**.

A caller running 10 000 findings through `ContainsLine` spends **≈0.5 ms** on
queries against the **≈155 µs** it already paid to build the set. There is no
batching decision to make here, and no reason to cache an answer that costs less
than the branch that would check the cache.

## 1. The resolution is paid once and is the only cost that matters

| | median | B/op | allocs/op |
|---|---|---|---|
| `GitResolve` (64 files × 4 hunks, parse + aggregation) | 155.3 µs | 62 588 | 786 |

That is **2.4 µs per changed file** and **12 allocations per file**, for a branch
touching 64 files across 8 package directories with 4 hunks each — a deliberately
busy shape.

**What this row does NOT include is the git subprocesses.** The payloads are
pre-rendered, so the number is pure parsing and aggregation. Real `Resolve` also
pays `git merge-base`, `git diff -U0 -M -C` and `git diff --name-status -z`,
which are *process spawns against a repository on disk* and will dominate this
figure by one to two orders of magnitude on any real checkout. The benchmark is
hermetic on purpose — it measures the part this package can regress, not the part
git owns.

So the honest reading of 155 µs is: **the parser is not where a slow
changed-set comes from.** If `Resolve` feels slow, it is the subprocesses or the
repository, and profiling the parser will find nothing.

## 2. The queries are flat, allocation-free, and effectively equal

| query | median | spread | B/op | allocs/op |
|---|---|---|---|---|
| `ContainsDir` (touched directory) | 32.4 ns | 9.3 % | 0 | 0 |
| `ContainsFile` (changed file) | 45.9 ns | 13.5 % | 0 | 0 |
| `ContainsLine` — hit | 48.9 ns | 18.2 % | 0 | 0 |
| `ContainsLine` — miss, untouched file | 48.0 ns | 12.8 % | 0 | 0 |
| `ContainsLine` — miss, unchanged line in a changed file | 50.0 ns | 12.2 % | 0 | 0 |

Three things a caller can rely on:

**Zero allocations on every query.** Nothing in the read path builds a string, a
slice or a map entry. A query loop adds no GC pressure at all, which is why
"query freely" is safe advice rather than a hope.

**A miss is not cheaper than a hit, and does not need to be.** The three
`ContainsLine` rows are within 4 % of each other — 48.0, 48.9 and 50.0 ns. The
*untouched file* miss returns at the map lookup; the *unchanged line* miss walks
all four folded ranges for that file and still lands at 50 ns, because four range
comparisons cost less than the map lookup that precedes them. A caller does not
have to order its questions to put the cheap case first.

**`ContainsDir` is the cheapest and is the right first question.** At 32.4 ns it
is ~30 % under the file query, because it reads one map and compares nothing.
A package-scoped analyser that asks `ContainsDir` before walking a directory
skips the per-file and per-line questions entirely for every package the branch
did not touch.

## 3. The arithmetic, for a caller sizing a run

A linter run over a 64-file branch, in round numbers:

| work | count | unit | total |
|---|---|---|---|
| resolve the changed set | 1 | 155 µs | **155 µs** |
| `ContainsDir` per package | 100 | 32 ns | 3.2 µs |
| `ContainsFile` per file | 2 000 | 46 ns | 92 µs |
| `ContainsLine` per finding | 10 000 | 49 ns | 490 µs |

**≈740 µs total**, of which the changed-set is ≈155 µs. Against a linter run
measured in seconds, the whole changed-set mechanism is noise — and that is the
point of the measurement: it says there is nothing here to optimise, with the
numbers rather than with an assurance.

## Platform note

The `-12` suffix is `GOMAXPROCS`, not parallelism in the benchmarks: every row
here is single-goroutine. `ChangedSetValue` is not safe for concurrent
modification and the queries are reads over maps that are complete before any
query runs — `Resolve` returns a finished value, and the suite pins that.

## Reproducibility envelope

> Every figure is the **median of 8 full runs**, each a separate process. The
> query rows are stable in shape and noisy in the last digit: their spreads
> (9–18 %) are dominated by frequency scaling on a laptop CPU, not by anything
> the code does — the B/op and allocs/op columns are **exactly 0 in all 8 runs**,
> and the ordering of the five query rows never changed. Read them as "≈30 ns"
> and "≈50 ns", not as three significant figures.
>
> **The ratios are what this report asserts**: ≈3 180× between one resolution and
> one query, and ≈30 % between `ContainsDir` and `ContainsLine`. Those held
> across all 8 runs. The absolute 155 µs will move with the payload shape — it is
> linear in changed files and in hunks per file, both of which the constants at
> the top of `diff_parse_bench_test.go` set.
>
> **This is a laptop measurement, and it is stamped as one.** A comparison
> against a figure from another machine says nothing (rule 9); regenerate on
> yours before drawing a delta.

| Dimension | Value |
|---|---|
| CPU | 12th Gen Intel(R) Core(TM) i7-1255U (12 cores visible) |
| RAM | 15 GiB |
| OS / kernel | Linux 6.12.107+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.0 linux/amd64 |
| Git branch | fix/vcs-git-bench-md |
| Git commit | 13f0ee3 |
| Generated (UTC) | 2026-09-12 |
| Bench wall-clock | `-benchtime=1s`, median of 8 runs |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/vcs/git
cpu: 12th Gen Intel(R) Core(TM) i7-1255U

benchmark                                   median ns/op   spread   min–max              B/op   allocs/op
GitResolve-12                                    155 300    10.5%   143 600 – 159 900   62 588     786
ChangedSetContainsLine/hit-12                      48.89    18.2%     42.70 – 51.61          0       0
ChangedSetContainsLine/miss_unchanged_file-12      48.00    12.8%     42.77 – 48.90          0       0
ChangedSetContainsLine/miss_unchanged_line-12      49.95    12.2%     46.05 – 52.12          0       0
ChangedSetContainsFile-12                          45.89    13.5%     44.42 – 50.61          0       0
ChangedSetContainsDir-12                           32.37     9.3%     31.03 – 34.04          0       0
```
