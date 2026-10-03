<!-- generated from internal/kernel/plugin/registry_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -count=10 -benchtime=200ms ./plugin/` to refresh -->
# Benchmarks — `internal/kernel/plugin`

`Registry[K, V]` replaced six hand-written copies of one mechanism — a snapshot
of a map, loaded, checked for nil and read — in the codec, writer, crypto,
transform, id and view registries (ADR 0159). Every dispatch through those
registries resolves a name first (`codec.Lookup` before a `Marshal`,
`crypto.LookupHasher` before a `Sum`), so `Lookup` is the one path of the table
that is hot, and the only number that could make the replacement a regression.
It is measured here beside the hand-written read, line for line, in the same
binary.

## Reproducibility envelope

> **These were taken on a busy machine.** The load average was between 26 and
> 43 on 10 cores while they ran — several builds of other branches shared it —
> so a single run swings widely. Each figure is the median of 20 runs, each run
> a fresh process, the two shapes interleaved run by run so both met the same
> load; the minimum is given beside it as the better estimate of intrinsic
> cost under contention.

| Dimension | Value |
|---|---|
| CPU cores          | 10 (Apple M1 Pro) |
| RAM                | 16 GiB |
| OS / kernel        | macOS 26.6.2 (Darwin 25.6.0) |
| Architecture       | arm64 |
| Go toolchain       | go1.27.1 darwin/arm64 |
| Git branch         | `refactor/sdk-tree-reorg--w4-p2d-registry` |
| Git commit         | `696cb29b` (pre-commit) |
| Generated (UTC)    | 2026-10-03 |
| Bench wall-clock   | `-benchtime=200ms -count=1`, 20 processes |
| Load average       | 26–43 on 10 cores |

## 1. `Lookup` costs what the hand-written read cost

A table of 16 entries — the size of the codec registry, the largest in the SDK.

| | median | min | B/op | allocs/op |
|---|---:|---:|---:|---:|
| `Lookup_Hit` | 8.92 ns | 8.27 ns | 0 | 0 |
| `Lookup_HitHandRolled` | 9.01 ns | 8.15 ns | 0 | 0 |
| `Lookup_Miss` | 6.21 ns | 5.76 ns | 0 | 0 |
| `Lookup_MissHandRolled` | 6.16 ns | 5.80 ns | 0 | 0 |
| `Lookup_Parallel` | 1.91 ns | 1.03 ns | 0 | 0 |
| `Lookup_ParallelHandRolled` | 2.25 ns | 1.70 ns | 0 | 0 |

Parity, within the noise in both directions, and nothing allocates. That is a
property of the compiler as much as of the table: `Lookup` is a generic method,
and `go build -gcflags=-m` on `internal/core/data/codec` reports it inlined
into `codec.Lookup` — and the snapshot's `Load` and the `atomic.Pointer` load
inlined into that — so a dispatch pays one atomic load and a map read, exactly
the hand-written code it replaced.

## 2. The six registries, before and after, through their public `Lookup`

Measured out of tree: a throwaway external test in each core package —
sixteen plug-ins registered through the package's own `Register`, then its
`Lookup` on a hit, a miss and in parallel (and codec's `LookupMIME` and
`LookupExt`) — compiled once against the tree before the change and once after,
the two binaries alternated for 20 rounds. Minimum per round, in ns:

| Registry | hit before | hit after | miss before | miss after | parallel before | parallel after |
|---|---:|---:|---:|---:|---:|---:|
| `core/data/codec` | 7.98 | 7.42 | 6.34 | 5.78 | 1.70 | 1.79 |
| `core/crypto` (`LookupHasher`) | 8.25 | 8.38 | 6.77 | 6.13 | 1.96 | 1.33 |
| `core/observe/logger/writer` | 7.71 | 7.58 | 6.33 | 5.58 | 1.61 | 1.38 |
| `core/data/transform` | 7.34 | 7.33 | 5.66 | 5.60 | 1.41 | 1.61 |
| `core/app/id` | 7.31 | 7.29 | 5.69 | 5.72 | 1.56 | 1.40 |
| `core/app/view` | 7.51 | 7.39 | 5.97 | 6.43 | 1.73 | 1.29 |

`benchstat` over the 20 runs finds no difference in any row (every p > 0.24)
except one, an improvement: codec's `LookupExt` went from 59.3 ns to 32.8 ns
(minimum; median 166 → 78 ns, p = 0.001). The change did not set out to make
it faster, and `go build -gcflags=-m` reports the same calls inlined before and
after, so the gain is recorded here rather than explained. `LookupMIME`
(116 → 106 ns minimum) is dominated by `mime.ParseMediaType` and its 48-byte
allocation, unchanged.

## 3. `Names` allocates once

| | median | B/op | allocs/op |
|---|---:|---:|---:|
| `Names`, collected then sorted (before) | 576.4 ns | 496 | 5 |
| `Names`, into a slice sized once (after) | 428.9 ns | 256 | 1 |

The table's size is known when it is listed, so the slice is made once at that
size instead of grown by `slices.Collect` through four reallocations
(p = 0.000, 10 interleaved runs each). `Names` is what an `Available` call
makes, never a dispatch, so this is tidiness rather than a hot path — but it is
one allocation for every registry at once.

## What is deliberately not measured

- **`Publish` and `Claim`.** They run once per plug-in at import, copying the
  whole table each time — the cost `internal/kernel/concur/snapshot/BENCH.md`
  measures as `Update_WithClone` (about 4 µs for 64 entries). A registry is
  written a few dozen times in a process's life.
- **Large tables.** A map read is constant-time; a 10 000-entry table would
  measure the map, and no registry holds more than sixteen.
