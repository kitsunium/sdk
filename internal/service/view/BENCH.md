<!-- generated from internal/service/view/view_bench_test.go — refresh with `cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./view/` -->
# Benchmarks — `internal/service/view`

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
| Git branch        | `agent-aa1fdf87a92fea84a` |
| Git commit        | `0b9dee0` (the tree this domain was added to) |
| Generated (UTC)   | 2026-09-10 |
| Bench wall-clock  | `-benchtime=1s -count=5`, **median quoted** |

The box is a shared VM with a memory balloon; the spread between the fastest
and slowest of five samples reaches 20 % on the render benchmarks. Every
conclusion below is drawn from a ratio measured **within one run**, never from
a single absolute number.

## The workload

One page shaped like a real one — a `<title>`, an `{{if}}`-free layout, a
`{{range}}` over twenty rows that each `{{template}}` a partial, and values
landing in four different escaping contexts (between tags, inside an
attribute, inside a URL, inside `<script>`). The rendered document is ≈ 2 kB.

Two tree sizes are measured, because reparsing costs **O(tree)** while
rendering costs **O(page)**:

- **small** — the two files the page needs.
- **large** — the same two plus forty filler templates, which is a small site.

## Headline: parse once, or pay 34×

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `RenderParsedOnceLargeTree` | 130 894 | 15 082 | 526 |
| `RenderReparsedEveryTimeLargeTree` | **4 434 953** | **519 457** | **4 252** |
| `RenderParsedOnce` (small tree) | 139 719 | 15 083 | 526 |
| `RenderReparsedEveryTime` (small tree) | 239 149 | 39 198 | 821 |
| `Construct` (small tree, no render) | 80 670 | 23 853 | 295 |

Constructing a `Renderer` per request instead of once, on a forty-two-template
tree, costs **33.9× the time, 34.4× the bytes and 8.1× the allocations**. It
is not one page being slower; it is the whole tree being read, parsed and
escape-analysed before the one page anybody asked for is rendered.

**The ratio is a function of tree size, and that is the part worth carrying
away.** On the two-file tree the same mistake costs only 1.7×, which is small
enough to survive a code review and small enough to look like noise in
staging. It reaches 34× on a tree that is still small by production standards,
and it keeps going. A measurement taken on a toy tree understates this defect
by exactly the factor the real tree is bigger.

Hence the rule, stated in `pkg/v1/view`'s package documentation, in this
package's `CLAUDE.md` and in ADR 0058 §D8: **build the `Renderer` at start-up
and keep it.** It is immutable after construction and safe for concurrent use,
so a package-level variable is the whole mechanism. There is deliberately no
reload, no lazy parse and no internal cache — each would put a mutex on the
read path to solve a problem a package variable already solves.

## What the SDK's contract costs over raw `html/template`

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `StdlibBaseline` (raw `html/template` → `bytes.Buffer`) | 132 854 | 13 007 | 524 |
| `RenderParsedOnceLargeTree` (the port, the scan, the ceiling, the copy-out) | 130 894 | 15 082 | 526 |

**Two allocations and 2 075 bytes.** The bytes are the rendered document
itself: `Render` copies the pooled scratch out before returning it, because the
scratch goes back to the pool and must not be aliased by anything the caller
holds. Time is indistinguishable from the baseline on this box.

That copy is the price of the all-or-nothing contract, and the `-memprofile`
says so precisely: `(*renderer).execute` accounts for **13.3 %** of all bytes
allocated during a render, and every other byte belongs to `text/template`.
It is measured and **deliberately not optimised away** — returning the pooled
buffer would make the next render overwrite the caller's document.

The CPU profile of `RenderParsedOnce` puts `html/template.Execute` at
**90.15 %** of total samples against `(*renderer).Render`'s 91.51 % cumulative:
the SDK's whole wrapper is **1.4 percentage points**, of which `limitWriter`
— the per-write ceiling check — is 0.77 % flat. Nothing else in this package
appears above the profiler's noise floor.

```
$ go tool pprof -top -cum /tmp/view.test /tmp/view-cpu.out
      flat  flat%   cum   cum%
     0.01s  0.19%  4.74s 91.51%  internal/service/view.(*renderer).Render
         0     0%  4.72s 91.12%  internal/service/view.(*renderer).execute
         0     0%  4.67s 90.15%  html/template.(*Template).Execute
     0.04s  0.77%  0.09s  1.74%  internal/service/view.(*limitWriter).Write
```

## The trust-type scan: free when the model is typed

Every render scans its model for the six refused `html/template` trust types
before the template runs. The cost is not uniform, and the difference is the
model's static TYPE:

| Benchmark (inert template, so the delta is the scan) | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `ScanGatePruned` — a typed view model | **445.9** | 152 | 4 |
| `ScanFullWalk` — the equivalent `map[string]any` | **13 657** | 835 | 46 |

A struct made of strings, ints and slices of those **cannot** hold a
`template.URL`, whatever its contents. That is a property of the type, so it
is computed once per type and cached: the scan then costs one map lookup and
touches no value at all. The 445.9 ns above is not the scan, it is the whole
of `Render` around an inert template — the scan is inside the noise.

A `map[string]any` gives the gate nothing to prune with. Every value's type is
only known at render time, so the walk visits all ~84 of them: **13.2 µs**,
about **10 %** of a real 130 µs render. That is the honest price of passing an
untyped model, and it is a reason to prefer a typed one that has nothing to do
with taste.

### Two optimisations, each measured against its own profile

The scan was profiled rather than guessed at. Both changes below come from a
`-memprofile`/`-cpuprofile` reading, and both are kept because the measurement
justified them:

| Version of `scan.go` | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| first working version | 22 200 | 2 245 | 133 |
| \+ `SetIterKey`/`SetIterValue` over map entries | 21 500 | 830 | **46** |
| \+ kind short-circuits before the type lookups | **13 657** | 835 | 46 |

1. **`alloc_objects` said 97.21 % of the scan's allocations were
   `reflect.unsafe_New`, reached through `walkMap`.** `MapIter.Key()` and
   `.Value()` box each entry into a fresh `reflect.Value` — twice per entry.
   `Value.SetIterKey`/`SetIterValue` reuse one addressable value per map:
   **133 → 46 allocations (−65 %), 2 245 → 830 B (−63 %)**. Time did not move,
   which is itself the finding: those allocations were small and short-lived.

2. **The CPU profile then showed the remaining time was type lookups**, not
   traversal — `canReachUnsafe`'s `sync.Map` load at 32.2 % cumulative plus the
   refused-type `map[reflect.Type]string` at 12.5 %, ≈ 45 % between them. Two
   facts remove most of both: an interface's own type is never one of the six
   and can always hold one, so both lookups are known answers; and all six
   refused types are defined **string** types, so no other kind can be in the
   table. Ordering `walk`'s checks by `reflect.Kind` first: **21 500 → 13 657
   ns/op (−36 %)**.

Net: **1.63× faster and 2.9× fewer allocations** than the first version that
worked, with no change to what the scan reports.

## Concurrency

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `RenderParsedOnce` (serial) | 139 719 | 15 083 | 526 |
| `RenderParallel` (8 vCPU) | 31 873 | 15 104 | 526 |

4.4× on 8 cores. The shortfall from 8× is not the SDK's: `html/template`'s
`Execute` takes its namespace mutex on every call to check the template's
escaping state. The pooled scratch adds no allocation under contention — the
per-op byte and allocation counts are unchanged from the serial run, which is
what a `recycler.CappedPool` is supposed to look like.

## Reproducing

```
cd internal/service
GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./view/

# the profiles quoted above
GOWORK=off go test -run '^$' -bench='^BenchmarkRenderParsedOnce$' -benchtime=3s \
  -cpuprofile=/tmp/view-cpu.out -memprofile=/tmp/view-mem.out -o /tmp/view.test ./view/
go tool pprof -top -cum /tmp/view.test /tmp/view-cpu.out
go tool pprof -top -sample_index=alloc_objects /tmp/view.test /tmp/view-mem.out
```
