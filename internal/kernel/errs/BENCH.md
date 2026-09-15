<!-- generated from internal/kernel/errs/*_bench_test.go — run `cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./errs/` to refresh -->
# Benchmarks — `internal/kernel/errs`

The SDK-wide typed-error package: dotted-quad `Code` (ADR 0005), the `*Error`
value with its Public/Private split and wrap trail, the `FieldValue` carrier,
the `PrefixMatcher` subnet router, and the read-only accessors (`CodeOf`,
`ReasonOf`, …). Every error in the SDK flows through here, so the surface splits
into two performance tiers these benchmarks lock in:

- **Zero-alloc hot paths** — `Code` bit-twiddling (`Pack`, `Major`/`Layer`/
  `Package`/`Serial`), the `*Error` field accessors (`Code`, `Reason`, `Public`,
  `Private`, `Source`, `Unwrap`, `HTTPStatus`, `ExitCode`), `HasCode`,
  `errors.Is` against a prefix/sentinel/pointer, the typed `Field*` constructors,
  and `PrefixMatcher` `Prefix`/`Mask`. All **0 allocs/op** — the contract a hot
  logging path depends on.
- **Bounded-alloc construction & rendering** — `Define`/`NewError`/`NewRuntime`/
  `Wrap` allocate the heap `*Error` (1 alloc; `Wrap` with a cause/fields adds a
  few more), and the string renderers (`Code.String`, `Code.Padded`,
  `Error.Error`, `PrefixMatcher.String`/`Error`, `ParseCode` on the invalid
  path) allocate their result strings. The numbers document that cost so a
  regression that turns a hot accessor into an allocating call is visible.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU cores          | 12 (12th Gen Intel(R) Core(TM) i7-1255U) |
| RAM                | 15.3 GiB |
| OS / kernel        | Linux 6.12.107+deb13-amd64 (Debian 13 trixie) |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | fix/bench-127 |
| Git commit         | c08d730 |
| Generated (UTC)    | 2026-09-15 |
| Bench wall-clock   | `-benchtime=1s -count=5`, median of 5 runs |

> **What these numbers support.** `allocs/op` is exact: all 5 repeats agreed on
> every cell, in every package. `B/op` is exact too **except where a cell
> carries `*`**, which marks five values that were not identical and a median
> reported in their place. Both columns are **unchanged** from a go1.26.4 run
> of this same code on this same box (124 benchmarks compared SDK-wide, 44 of
> them allocating, zero counter moved). `ns/op` are medians and carry the
> `spread` shown, which is a **within-run** figure that understates run-to-run
> variance: re-running the identical binary on this box moved individual cells
> by up to 94 %. Read ns/op as an order of magnitude on this box, never as a
> cross-edition or cross-machine delta.

> **This edition changes the reference platform.** The previous edition was
> measured on **arm64** (8-core, Linux 6.12.72-linuxkit) under **go1.26.4**; this
> one is **amd64** on the box stamped above. The two editions are NOT
> comparable — a reader drawing a delta across them would be measuring the
> architecture, not the code. The whole table was re-measured here rather
> than half-updated, so the cells stay comparable with each other.

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/kernel/errs
cpu: 12th Gen Intel(R) Core(TM) i7-1255U

benchmark                          median ns/op   spread   min–max           B/op   allocs/op
CodeOf-12                                 13.71     3.6%   13.49 – 13.98        0           0
CodeOf_Parallel-12                        48.07     6.5%   47.72 – 50.84        0           0
ReasonOf-12                               14.35     4.5%   14.16 – 14.80        0           0
ReasonOf_Parallel-12                      47.59     9.2%   45.03 – 49.42        0           0
PublicOf-12                               14.44     6.2%   13.72 – 14.61        0           0
PublicOf_Parallel-12                      21.46     0.8%   21.40 – 21.57        0           0
PrivateOf-12                              14.98     7.1%   14.91 – 15.97        0           0
PrivateOf_Parallel-12                     21.43     0.2%   21.39 – 21.44        0           0
FieldsOf-12                               104.8     4.9%   102.1 – 107.2       64           1
FieldsOf_Parallel-12                      67.94     0.9%   67.57 – 68.19       64           1
HTTPStatusOf-12                           15.79    15.5%   13.98 – 16.43        0           0
HTTPStatusOf_Parallel-12                  23.11     2.3%   22.94 – 23.46        0           0
ExitCodeOf-12                             17.80    24.2%   14.64 – 18.95        0           0
ExitCodeOf_Parallel-12                    23.27     1.8%   23.01 – 23.42        0           0
HasReason-12                              14.62    11.4%   13.63 – 15.30        0           0
HasReason_Parallel-12                     19.56     2.6%   19.19 – 19.69        0           0
TrailOf-12                                51.54     8.8%   49.27 – 53.81        8           1
TrailOf_Parallel-12                       44.73     5.7%   43.24 – 45.81        8           1
Pack-12                                  0.9415    10.2%   0.9195 – 1.016       0           0
Pack_Parallel-12                          4.898     0.7%   4.88 – 4.914         0           0
Code_Major-12                            0.9669     4.3%   0.9445 – 0.986       0           0
Code_Layer-12                             0.947     5.6%   0.9287 – 0.9817      0           0
Code_Package-12                          0.9523     4.9%   0.9195 – 0.9664      0           0
Code_Serial-12                           0.9838     9.7%   0.9578 – 1.053       0           0
Code_String-12                            90.40     6.5%   86.49 – 92.35        8           1
Code_String_Parallel-12                   49.10     6.1%   47.17 – 50.18        8           1
Code_Padded-12                            284.9     6.2%   275.5 – 293.3       28           5
Code_Padded_Parallel-12                   62.69     4.3%   62.33 – 65.01       28           5
Define-12                                 243.3     4.0%   239.8 – 249.5      144           1
Define_WithOptions-12                     257.2     5.2%   247.6 – 261.1      144           1
NewError-12                               244.7     9.2%   241.4 – 264.0      144           1
NewRuntime-12                             254.1     7.0%   240.8 – 258.6      144           1
Wrap_Stdlib-12                            270.2    19.5%   232.4 – 285.2      144           1
Wrap_SDKCause-12                          230.7     2.3%   228.5 – 233.9      152           2
Wrap_WithFields-12                        376.3     5.2%   372.4 – 391.9      280           3
Error_Code-12                            0.9563    12.0%   0.9395 – 1.054       0           0
Error_Code_Parallel-12                    4.922     1.7%   4.865 – 4.948        0           0
Error_Reason-12                           1.366    23.6%   1.288 – 1.61         0           0
Error_Public-12                           1.313     4.4%   1.285 – 1.343        0           0
Error_Private-12                          1.304     3.5%   1.279 – 1.325        0           0
Error_Fields-12                           80.38     3.3%   79.51 – 82.19       64           1
Error_HTTPStatus-12                       1.017    20.8%   0.9431 – 1.155       0           0
Error_ExitCode-12                        0.9988    22.5%   0.927 – 1.152        0           0
Error_Error_NoTrail-12                    232.5     6.7%   222.8 – 238.3       56           2
Error_Error_NoTrail_Parallel-12           68.84     3.3%   67.57 – 69.84       56           2
Error_Error_Trail-12                      363.8     6.1%   351.6 – 373.7       96           3
Error_Source-12                           1.404    19.4%   1.344 – 1.616        0           0
Error_Unwrap-12                            1.64    31.1%   1.592 – 2.102        0           0
HasCode-12                                15.97    14.0%   14.35 – 16.59        0           0
HasCode_Parallel-12                       18.18     1.5%   18.03 – 18.30        0           0
HasCode_Trail-12                          3.741    38.6%   3.699 – 5.142        0           0
Error_Is_Prefix-12                        2.798     6.9%   2.77 – 2.964         0           0
Error_Is_Sentinel-12                      5.406    22.4%   5.148 – 6.36         0           0
Error_Is_Pointer-12                       2.634     5.0%   2.528 – 2.659        0           0
FieldString-12                            11.12     4.9%   11.04 – 11.59        0           0
FieldInt-12                               2.809     4.1%   2.75 – 2.866         0           0
FieldInt64-12                              2.94    31.0%   2.779 – 3.689        0           0
FieldBool-12                              2.994    25.4%   2.844 – 3.604        0           0
FieldFloat-12                             2.755     1.1%   2.747 – 2.776        0           0
NewFieldValue-12                          11.52     4.3%   11.26 – 11.76        0           0
FieldValue_Key-12                         1.237     9.5%   1.213 – 1.33         0           0
FieldValue_StringValue_String-12          3.682     6.2%   3.568 – 3.798        0           0
FieldValue_StringValue_Int-12             44.51    24.8%   38.64 – 49.68        8           1
FieldValue_StringValue_Float-12           104.6     9.8%   96.42 – 106.7        8           1
ParseCode-12                              44.76    10.1%   44.12 – 48.63        0           0
ParseCode_Parallel-12                     46.21     6.7%   44.87 – 47.96        0           0
ParseCode_Invalid-12                      308.9     8.9%   295.7 – 323.2      192           2
NewPrefixMatcher-12                       18.98     3.1%   18.76 – 19.35        8           1
PrefixMatcher_Prefix-12                  0.9459     6.3%   0.9392 – 0.9989      0           0
PrefixMatcher_Prefix_Parallel-12          4.903     2.0%   4.819 – 4.915        0           0
PrefixMatcher_Mask-12                    0.9927     7.1%   0.9467 – 1.017       0           0
PrefixMatcher_Mask_Parallel-12            4.921     1.4%   4.881 – 4.95         0           0
PrefixMatcher_String-12                   310.9    12.3%   303.3 – 341.4       72           4
PrefixMatcher_String_Parallel-12          87.20     1.5%   86.31 – 87.64       72           4
PrefixMatcher_Error-12                    313.8     6.2%   301.2 – 320.8       72           4
```

## How to read this

- **Accessors at ~1 ns / 0 allocs** (`Error_Code`, `Error_Reason`, `Pack`,
  `Field*`, `Error_Is_*`, `PrefixMatcher_Prefix`/`Mask`) — pure field reads / bit
  ops on an already-built value. The 0 allocs/op figure is the contract; any
  regression to > 0 allocs is a bug.
- **Typed-accessor helpers at ~14 ns / 0 allocs** (`CodeOf`, `ReasonOf`,
  `HasCode`, …) — one `errors.As`/type-assertion walk plus the field read; the
  `_Parallel` variants show the small contention overhead of concurrent walks.
- **Construction at ~240–255 ns / 1+ allocs** (`Define`, `NewError`,
  `NewRuntime`, `Wrap_*`) — the heap `*Error` is the unavoidable allocation;
  `Wrap` with an SDK cause or fields adds the trail/field slices. This is the
  floor a call site pays to *create* an error, not to inspect one.
- **Rendering / defensive-copy paths allocate** (`Code_String`, `Code_Padded`,
  `Error_Error_*`, `PrefixMatcher_String`/`Error`, `ParseCode_Invalid`,
  `FieldValue_StringValue_*`, `TrailOf`, `FieldsOf`, `Error_Fields`) — they build
  result strings or clone slices, so non-zero B/op and allocs/op are expected and
  documented here rather than hidden. `FieldsOf` / `Error_Fields` are benched
  against a fixture that actually carries a field, so they measure the real
  `slices.Clone` cost (1 alloc) rather than the nil/empty fast path.
