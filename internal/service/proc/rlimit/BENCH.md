<!-- generated from internal/service/proc/rlimit/rlimit_bench_test.go + rlimit_linux_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./proc/rlimit/` to refresh -->
# Benchmarks — `internal/service/proc/rlimit`

**"`PrepareSysProcAttr` is documented as the fail-fast call to make at `Spec`
construction time. Is it cheap enough that I never have to think about it?"**

**Yes: 74 ns for one resource, 142 ns for four, and zero allocations at any
size.** And when the limit is actually applied, **79 % of the cost is the kernel**
— the package's own table lookup is the remaining 74 ns.

## 1. Validation is free, and it is the same lookup `Apply` does

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `PrepareSysProcAttr_Nil` — no limits requested | **9.608** | 0 | 0 |
| `PrepareSysProcAttr_One` | 74.48 | 0 | 0 |
| `PrepareSysProcAttr_Four` | 141.5 | 0 | 0 |
| `PrepareSysProcAttr_Unmapped` — the refusal | 66.95 | 0 | 0 |

The slope is **≈22 ns per resource** on a **≈52 ns** fixed base. That base is not
the map lookup — it is Go's `range` over a map, whose iterator setup (including
the start-position randomisation) dominates a one-element map. The lookup itself
is the 22 ns.

Two things follow:

- **`PrepareSysProcAttr(nil)` is 9.6 ns**, so calling it unconditionally on every
  `Spec` — even the overwhelming majority that request no limits — costs nothing.
  That is what makes the fail-fast advice in the package doc actually free to
  follow.
- **The refusal path is CHEAPER than the success path** (67 ns vs 74 ns) because
  it short-circuits on the first unmapped resource. A validator whose cost the
  caller's input can inflate is a validator worth checking; this one cannot be.

Zero allocations everywhere, including the refusal — `UnknownResource` is a bare
sentinel with no cause to wrap.

## 2. Applying a limit: 79 % kernel, and `prlimit64` costs 54 % more than `setrlimit`

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Apply_Self` — `setrlimit(2)`, pid 0 | **352.9** | 0 | 0 |
| `Apply_ForeignPID` — `prlimit64(2)` against an owned child | **544.9** | 0 | 0 |
| `Apply_Unmapped` — refused before any syscall | 69.64 | 0 | 0 |

`Apply_Unmapped` (69.6 ns) is within noise of `PrepareSysProcAttr_Unmapped`
(67.0 ns) — the same table walk, confirming the two entry points share one
mechanism and that the rows are measuring what they claim.

Subtracting the table walk from the syscall rows:

```
352.9 − 74.5 = 278 ns  →  setrlimit(2)      = 79 % of Apply_Self
544.9 − 74.5 = 470 ns  →  prlimit64(2)      = 86 % of Apply_ForeignPID
```

**`prlimit64` is 1.7× the cost of `setrlimit`** for the identical limit. That is
the kernel finding and locking a foreign task rather than operating on `current`,
and it is the price of the flexibility the package offers: pid 0 is the cheap
path and any other pid is not. A supervisor applying limits to a child it just
spawned takes the expensive one — 545 ns against the ~626 µs the spawn itself cost
(`exec/BENCH.md` §1), so **0.09 %**. It does not matter, and now it is known not
to matter rather than assumed.

Both are allocation-free. `syscall.Rlimit` is stack-allocated and passed by
pointer to a syscall that does not let it escape.

### `prlimit64` on an unprivileged host — measured, not assumed

The package doc says `prlimit64(2)` "needs `CAP_SYS_RESOURCE`". That is the
sufficient condition, not the necessary one: `prlimit(2)` also permits it when the
caller's real/effective/saved uids match the target's. `BenchmarkApply_ForeignPID`
spawns its own child and applies the child's own current ceiling to it, and **it
succeeds in this unprivileged container** — 2.1 million iterations, no `EPERM`.
The benchmark skips with the kernel's reason rather than publishing the cost of a
refusal if a host does deny it.

## 3. Nothing to optimise

The three components of this package are a map lookup (22 ns), a `range` over a
tiny map (52 ns), and a syscall (278–470 ns). There is no allocation anywhere and
no path that scales with anything but the number of resources requested — which is
bounded by the size of the `Resource` enum.

The only line a profile could name is the map `range`, and replacing
`map[Resource]int` with a dense array indexed by the enum would save perhaps 40 ns
on a call whose whole job is a 278 ns syscall. **Not done.** The measurement that
matters is §2's: the caller is paying the kernel, and the kernel's price is
already the floor.

No production code was changed in this package.

## Platform coverage

`rlimit_bench_test.go` is untagged and covers `PrepareSysProcAttr` on every GOOS
the package compiles on; it skips where the platform reports
`UnsupportedPlatform`, so the numbers are never the cost of a `!unix` stub
returning a sentinel. `rlimit_linux_bench_test.go` is `//go:build linux` because
`prlimit64(2)` is Linux-only — off Linux a foreign pid is `UnsupportedPlatform` by
design (ADR 0018), and there would be nothing to measure.

Both `Apply` rows re-apply the resource's **current** soft/hard pair, read with
`getrlimit(2)` before the timer starts. Every iteration is a real syscall that
changes nothing observable — the only honest way to issue millions of `setrlimit`
calls without walking a ceiling downwards.

## Reproducibility envelope

> Every figure is the **median of 3 full runs**, each a separate process. Spread
> was **under 2.5 % on every row** — the tightest set in this domain, which is
> what a package with no allocations and no scheduler interaction looks like.
> Load average was 1.64 during these runs on this 8-core box.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor (8 cores visible) |
| RAM | 15 GiB |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | jaimerias-que-tu-te-connect |
| Git commit | 532a984 |
| Generated (UTC) | 2026-09-10 |
| Bench wall-clock | `-benchtime=1s`, median of 3 runs, 1-min load ~1.6 |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/proc/rlimit
cpu: AMD EPYC 7351P 16-Core Processor

benchmark                        median ns/op   spread   B/op  allocs/op
PrepareSysProcAttr_Nil-8                9.608     1.4%      0      0
PrepareSysProcAttr_One-8                74.48     0.1%      0      0
PrepareSysProcAttr_Four-8               141.5     2.4%      0      0
PrepareSysProcAttr_Unmapped-8           66.95     1.6%      0      0
Apply_Self-8                            352.9     1.1%      0      0
Apply_ForeignPID-8                      544.9     0.5%      0      0
Apply_Unmapped-8                        69.64     0.5%      0      0
```
