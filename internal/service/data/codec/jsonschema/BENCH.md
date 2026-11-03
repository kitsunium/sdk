<!-- generated from internal/service/data/codec/jsonschema/jsonschema_bench_test.go — run `go test -run='^$' -bench=. -benchmem -benchtime=300ms -count=6 ./internal/service/data/codec/jsonschema/` to refresh -->
# Benchmarks — `internal/service/data/codec/jsonschema`

| | |
|---|---|
| Machine | AMD EPYC 7351P 16-Core Processor, 8 vCPU, 15 GiB |
| OS | Linux 6.12.101+deb13-amd64 |
| Go toolchain | go1.27.3 linux/amd64 |
| Base commit | `740e7c1e` (+ the change measured below) |
| Measured | 2026-11-03, `-benchtime=300ms -count=6`, medians |

> **Numbers vary across machines.** Compare deltas measured on one machine,
> never figures from two.

| benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `NewValidator` (the order schema of the tests: `$defs`, `$ref`, a pattern) | 103 400 | 23 460 | 431 |
| `Validate10KiB` (a valid order of 220 items, 10.4 KiB; ≈ 6 MB/s) | 893 000 | 219 340 | 5 043 |

Compiling is a once-per-schema cost. Validating reads the document twice —
strictjson's bounded read into a raw value, then the exact tree (one `big.Rat`
per number) — and builds every member's and element's path as it visits it,
so a valid document pays for the paths a report would need. About 23
allocations per item here. A validator that kept the paths lazy and the
numbers as literals until a numeric keyword asks would be the next lever.
