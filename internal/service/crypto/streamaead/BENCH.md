<!-- generated from internal/service/crypto/streamaead/streamaead_bench_test.go — run `cd internal/service && GOWORK=off go test -bench=. -benchmem -benchtime=1s ./crypto/streamaead/...` to refresh -->
# Benchmarks — `internal/service/crypto/streamaead`

Streaming AES-256-GCM (ADR 0014 §D2). Throughput-oriented: each iteration seals
or opens a 1 MiB payload through the fixed 64 KiB chunked frame.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU                | 12th Gen Intel(R) Core(TM) i7-1255U |
| CPU cores          | 12 |
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

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/crypto/streamaead
cpu: 12th Gen Intel(R) Core(TM) i7-1255U

benchmark            median ns/op   spread   min–max                       B/op   allocs/op   median MB/s
StreamSeal_1MiB-12      2 536 789    11.5%   2 386 609 – 2 677 398   *3 533 832          64          413.4
StreamOpen_1MiB-12      1 013 978     5.6%   977 094 – 1 033 861     *1 125 904          55          1 034

* marks a B/op cell whose value was not identical across the 5 repeats;
  the median is shown. allocs/op was unanimous on every row.
```

## How to read this

- **Throughput** (`MB/s`) is the headline number for a streaming AEAD: ~413 MB/s
  seal, ~1 034 MB/s open on this box, dominated by hardware AES-NI + the SHA-256
  HKDF key-derivation done once per stream.
- **allocs/op** reflects the per-1-MiB stream cost: the open path holds one
  reusable wire buffer allocated once after the header verifies (each chunk is
  read in place into it), and each chunk's `Open` returns a fresh plaintext slice.
  The construction favours bounded memory (a single chunk-sized wire buffer plus
  the current chunk's plaintext) over zero-alloc — the whole point of streaming is
  to avoid holding the full payload twice.
- Median of 5 repeats (`-count=5`); the per-row `spread` column is the
  within-run min-max. Between-run variance on this box is larger than that
  spread — see the envelope note above before citing any ns/op.
