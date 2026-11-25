<!-- generated from internal/kernel/collections/frozen/frozen_bench_test.go — run `cd internal && GOWORK=off go test -run='^$' -bench='Get|Load|Build' -benchtime=200ms -count=6 ./kernel/collections/frozen/` and benchstat `-col /impl` to refresh -->
# Benchmarks — `internal/kernel/collections/frozen`

Read-only maps over a minimal perfect hash. Two questions matter: is a lookup
faster than a native `map` and than the frozen table it was measured against
(`cnfgo`'s `FrozenMap`, v0.1.0), and what does it cost to hold and to build.

## Reproducibility envelope

> **Numbers vary across machines — and on this run, across minutes.** The
> machine was shared with other agents' builds and benchmarks the whole time
> (load average 10 to 30 on 10 cores, under 10 % of the memory free). The
> spread benchstat prints is that noise; a cell whose spread passes ±50 % is
> not a measurement, and is said so below. Re-run on a quiet machine before
> quoting one of them.

| Dimension | Value |
|---|---|
| CPU                | Apple M1 Pro, 10 cores |
| RAM                | 16 GiB |
| OS                 | macOS 26.6.2 |
| Architecture       | arm64 |
| Go toolchain       | go1.27.4 darwin/arm64 |
| Git commit         | `152b37038` + this change (pre-commit) |
| Generated (UTC)    | 2026-11-25 |
| Bench wall-clock   | `-benchtime=200ms` (Get), `3x` (Build), `-count=10` (Get vs cnfgo), `-count=6` otherwise |
| Keys               | random `uint64`s; strings `"k/"` + base 36 of a random 52-bit number (8 to 13 bytes); probes shuffled, string probes in allocations of their own (a probe sharing the stored key's bytes lets a native map's comparison stop at the pointers) |

## Lookups against cnfgo and a native map

The comparison with `cnfgo` runs in a module outside the SDK (the SDK takes no
dependency), through cnfgo's public API only (`cnfrt.NewFrozenMap(src, nil)`,
`Get`), on the same keys and probes: `frozen` is `NewMap` / `NewStringMap`
(values `uint64`), `cnfgo` is `FrozenMap[uint64, uint64]` /
`FrozenMap[string, uint64]`, `native` is `map[K]uint64`.

```
                                       │    frozen     │                  cnfgo                   │                 native                 │
                                       │    sec/op     │     sec/op       vs base                 │    sec/op      vs base                 │
Get/n=1000/key=uint64/probe=hit-10       8.968n ±   6%   11.660n ±   21%   +30.01% (p=0.000 n=10)   11.435n ± 36%   +27.50% (p=0.003 n=10)
Get/n=1000/key=uint64/probe=miss-10      6.982n ±  20%    8.845n ±   10%   +26.67% (p=0.000 n=10)    7.448n ±  6%         ~ (p=0.218 n=10)
Get/n=1000/key=string/probe=hit-10       15.67n ±   4%    22.69n ±   28%   +44.85% (p=0.000 n=10)    14.49n ± 21%         ~ (p=0.143 n=10)
Get/n=1000/key=string/probe=miss-10      7.789n ±   6%   12.965n ±    5%   +66.45% (p=0.000 n=10)   11.305n ± 35%   +45.14% (p=0.000 n=10)
Get/n=100000/key=uint64/probe=hit-10     21.25n ±  48%    33.88n ±   60%   +59.42% (p=0.009 n=10)    16.29n ± 41%         ~ (p=0.218 n=10)
Get/n=100000/key=uint64/probe=miss-10    7.510n ±   3%   20.265n ±   10%  +169.84% (p=0.000 n=10)   26.390n ± 15%  +251.40% (p=0.000 n=10)
Get/n=100000/key=string/probe=hit-10     60.31n ± 123%    47.70n ±   69%         ~ (p=0.247 n=10)    28.52n ± 20%   -52.72% (p=0.000 n=10)
Get/n=100000/key=string/probe=miss-10    9.142n ±   5%   27.775n ±   10%  +203.82% (p=0.000 n=10)   45.920n ± 77%  +402.30% (p=0.000 n=10)
Get/n=1000000/key=uint64/probe=hit-10    46.12n ±  24%   156.25n ±  197%  +238.75% (p=0.000 n=10)    63.81n ± 29%   +38.35% (p=0.009 n=10)
Get/n=1000000/key=uint64/probe=miss-10   12.80n ±  55%    48.35n ± 1395%  +277.73% (p=0.001 n=10)    44.05n ± 60%  +244.14% (p=0.002 n=10)
Get/n=1000000/key=string/probe=hit-10    107.8n ±  47%    220.1n ±  534%  +104.17% (p=0.000 n=10)    302.1n ± 41%  +180.19% (p=0.000 n=10)
Get/n=1000000/key=string/probe=miss-10   14.79n ±  24%    32.70n ±   30%  +120.99% (p=0.000 n=10)    56.95n ± 19%  +284.93% (p=0.000 n=10)
geomean                                  17.12n           32.71n           +91.08%                   29.17n         +70.40%
```

`Get/n=100000/key=string/probe=hit` was run again alone, its first spread
being ±123 %: 24.9 ns (frozen) ±22 %, 41.4 ns (cnfgo) ±97 %, 68.4 ns (native)
±56 % — a noise-bound cell either way.

**Where frozen wins:** every miss, by 1.3× to 3.8× against cnfgo and up to 5×
against the native map — the tag byte refuses an absent key before any record
is read; integer hits against cnfgo (1.3× at 1 000 keys, 1.6× at 100 000,
3.4× at a million, where cnfgo's spread is ±197 %); string hits against cnfgo
at 1 000 keys (1.45×), and 2× at a million inside cnfgo's ±534 %.
**Where it does not:** a native map is as fast or faster on hits at 1 000 keys
(string 15.7 against 14.5 ns, within noise) and at 100 000 integer keys
(21.3 ± 48 % against 16.3 ± 41 % ns, not significant); the native string hit
at 100 000 keys (28.5 ns) beat frozen's first run, and the rerun reversed it —
noise, not a result.

## Build

```
                         │     frozen     │                 cnfgo                 │                native                │
                         │     sec/op     │     sec/op      vs base               │    sec/op      vs base               │
BuildUint64/n=1000-10       83.44µ ±  11%    57.98µ ±  23%  -30.51% (p=0.002 n=6)   11.66µ ±  69%  -86.03% (p=0.002 n=6)
BuildUint64/n=100000-10    11.123m ±  22%    5.530m ±  32%  -50.29% (p=0.002 n=6)   1.217m ±   7%  -89.05% (p=0.002 n=6)
BuildUint64/n=1000000-10   242.76m ± 312%   229.79m ± 113%        ~ (p=0.485 n=6)   78.01m ±  48%  -67.86% (p=0.002 n=6)
BuildString/n=1000-10      124.86µ ± 134%    90.75µ ± 432%        ~ (p=0.310 n=6)   22.81µ ±  29%  -81.73% (p=0.002 n=6)
BuildString/n=100000-10    16.292m ±  95%    6.836m ± 149%  -58.04% (p=0.015 n=6)   2.764m ±  20%  -83.03% (p=0.002 n=6)
BuildString/n=1000000-10    288.9m ±  20%    288.7m ± 565%        ~ (p=0.937 n=6)   141.1m ± 149%        ~ (p=0.065 n=6)
geomean                     7.139m           4.861m         -31.91%                 1.464m         -79.49%
```

**A build is slower than cnfgo's at 1 000 and 100 000 keys** (1.4× and 2×,
uint64; 2.4× for strings at 100 000) and level at a million, where both
spreads pass 100 %. The search for pilots is the price of a table with no
empty slot. It allocates 2.3× to 2.6× less than cnfgo's build, in 21
allocations whatever the size.

## Memory

Measured after two collections, with and without one live table
(`runtime.MemStats.HeapAlloc`, and `/gc/scan/heap:bytes` for what the
collector scans), the same million keys; the image is `MarshalBinary` /
`MarshalCNF`.

| n = 1 000 000 | heap / entry | scanned / entry | image / entry |
|---|---|---|---|
| uint64 → uint64, frozen | 17.5 B | 0 | 17.5 B |
| uint64 → uint64, cnfgo  | 52.2 B | 0 | 51.7 B |
| uint64 → uint64, native | 37.8 B | 0.07 B | — |
| string → uint64, frozen | 29.6 B | 0 | 29.6 B |
| string → uint64, cnfgo  | 56.3 B | 0 | 55.8 B |
| string → uint64, native | 55.9 B | 55.9 B | — |

At 100 000 keys: 17.5 / 39.8 / 23.7 B (uint64) and 29.7 / 44.2 / 35.0 B
(string). frozen's string figures include the keys' bytes, copied into its
image; the native map's do not (its headers point at the caller's strings),
so its real cost is higher than shown. A frozen table holds six pointers
whatever its size.

## In-package numbers

`frozen_bench_test.go`, against the native map only:

```
                                       │    frozen     │                 native                 │        
                                       │    sec/op     │     sec/op      vs base                │    sec/
Get/n=1000/key=uint64/probe=hit-10       9.034n ±   7%    8.286n ±  12%    -8.28% (p=0.004 n=6)
Get/n=1000/key=uint64/probe=miss-10      6.537n ±  15%    7.782n ±  17%   +19.04% (p=0.002 n=6)
Get/n=1000/key=string/probe=hit-10       15.88n ±  16%    15.18n ±  23%         ~ (p=0.240 n=6)
Get/n=1000/key=string/probe=miss-10      7.976n ±  13%   23.115n ± 125%  +189.81% (p=0.002 n=6)
Get/n=100000/key=uint64/probe=hit-10     38.36n ±  56%    17.83n ±  39%         ~ (p=0.093 n=6)
Get/n=100000/key=uint64/probe=miss-10    8.692n ±  10%   27.395n ±  15%  +215.16% (p=0.002 n=6)
Get/n=100000/key=string/probe=hit-10     34.86n ±  76%    37.23n ±  81%         ~ (p=0.937 n=6)
Get/n=100000/key=string/probe=miss-10    8.784n ±  26%   39.710n ±  35%  +352.07% (p=0.002 n=6)
Get/n=1000000/key=uint64/probe=hit-10    37.50n ± 161%    55.74n ±  63%         ~ (p=0.065 n=6)
Get/n=1000000/key=uint64/probe=miss-10   11.13n ±  19%    60.12n ±  20%  +440.09% (p=0.002 n=6)
Get/n=1000000/key=string/probe=hit-10    156.9n ±  35%    350.3n ± 143%  +123.30% (p=0.002 n=6)
Get/n=1000000/key=string/probe=miss-10   31.51n ±  51%   254.35n ±  87%  +707.33% (p=0.002 n=6)
Load/n=1000/check=header-10                                                                       221.2n
```

`Load` of a `StringMap` image: `CheckHeader` 117 to 228 ns whatever the size
(the header, and the table allocated); `CheckAll` 16 µs at 1 000 keys, 2.8 ms
at 100 000 and 15.8 ms at a million — about 1.7 GiB/s, the CRC and the walk
that rehashes every key.
