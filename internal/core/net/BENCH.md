<!-- generated from internal/core/net/sse_bench_test.go — run `cd internal/core && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s -count=3 ./net/` to refresh; every published number is the MEDIAN of the three -->
# Benchmarks — `internal/core/net`

One benchmark lives here: `SSEEventValue.Validate`, the check every
Server-Sent Events frame passes before a byte of it is written. It is the one
per-frame cost the contract layer owns.

## Where the rest went

This file used to open with the per-byte costs of the two wire formats — the
WebSocket mask, UTF-8 check and frame codec, and the SSE terminator scan.
Reading and writing a wire format is a mechanism, not the domain's contract,
so the code moved to the services that speak each format (ADR 0160 §4), and
its benchmarks and measurements moved with it:

- `internal/service/net/websocket/BENCH.md` §The wire format — the
  word-at-a-time mask (16.1× at 4 KiB), the 28× cost of multibyte UTF-8, the
  per-frame rows, the refused short-payload branch and the in-situ result;
- `internal/service/net/sse/BENCH.md` §The encoder — the `IndexAny` profile,
  the 1.6×–4.6× encoder rows and the two quadratic scans that were refused.

## Validate: two byte searches, and a guard on the empty string

| `SSEEventValue.Validate` | before | after | |
|---|---:|---:|---:|
| id + event | 62.96 ns | **36.33 ns** | 1.73× |
| data only | 20.86 ns | **12.16 ns** | 1.72× |

`Validate` calls `validateSSELine` for
`id` and for `event` on EVERY frame, present or not, and `strings.ContainsAny`
is `IndexAny` again. The substitution is the same one
`internal/service/proc/systemd/notify` already documents ("two byte searches, not
ContainsAny … 24 % of this function"). One extra guard earned its place there:
on the EMPTY string `ContainsAny` returns without looking at anything while two
`IndexByte` calls still happen, so the naive substitution made a data-only
`Validate` **0.80×** — 26.2 ns against 20.9. Guarding on `value != ""` first
took it to 12.2 ns and costs nothing measurable where the value is present.
That row is published because it was the one row of this change that measured
SLOWER, and it stayed slower until it was looked at. The encoder's own comment
check (`internal/service/net/sse`) is spelled the same way, for the same
reason.

Zero allocations on both rows: a refusal wraps a sentinel, and the accepting
path builds nothing.

## Reproducibility envelope

> **Numbers vary across machines**; the allocation column is exact, and what
> this file asserts is the ratio. Both rows are the median of three runs at
> `-benchtime=1s` and sit inside 6 % across them — tens of nanoseconds, where
> the timer's own resolution is a visible share.

| Dimension | Value |
|---|---|
| CPU cores          | 8 |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.27.1 linux/amd64 |
| Git branch         | jaimerias-que-tu-te-connect |
| Git commit         | f2d263d |
| Generated (UTC)    | 2026-09-10 |
| Bench wall-clock   | `-test.benchtime=1s`, `-test.count=3`, medians |

## Results

Medians of three at `-benchtime=1s`. The `before` block is `git show
HEAD:internal/core/net/sse.go` restored into the tree and re-measured on the
same binary; the `after` block is what ships.

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/core/net
cpu: AMD EPYC 7351P 16-Core Processor
                                            BEFORE          AFTER
BenchmarkSSEValidate/id_and_name              62.96 ns/op    36.33 ns/op   0 B/op  0 allocs/op
BenchmarkSSEValidate/data_only                20.86 ns/op    12.16 ns/op   0 B/op  0 allocs/op
```
