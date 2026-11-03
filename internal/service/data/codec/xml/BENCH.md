<!-- generated from internal/service/data/codec/xml/append_bench_test.go — run `go test -run='^$' -bench='Marshal1KiB|Append1KiB' -benchmem -benchtime=500ms -count=6 ./internal/service/data/codec/xml/` to refresh -->
# Benchmarks — `internal/service/data/codec/xml`

| | |
|---|---|
| Machine | AMD EPYC 7351P 16-Core Processor, 8 vCPU, 15 GiB |
| OS | Linux 6.12.101+deb13-amd64 |
| Go toolchain | go1.27.3 linux/amd64 |
| Base commit | `740e7c1e` (+ the change measured below) |
| Measured | 2026-11-03, `-benchtime=500ms -count=6`, interleaved old/new, medians (benchstat) |

> **Numbers vary across machines.** Compare deltas measured on one machine,
> never figures from two.

## Marshal and Append of a value of about 1 KiB (#1124)

`Append` writes onto a caller buffer that already has the room, so its
figures are what Append costs beyond the bytes themselves.

The value is an element of about 1 KiB of XML (`kibValue`).

| benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Marshal1KiB` | 22 850 → 23 050 (~) | 5 857 → 5 857 | 10 → 10 |
| `Append1KiB` | 22 520 → 22 410 (~) | 4 576 → 4 576 | 9 → **9** |

`Append` used to encode into a pooled buffer and copy the result onto `dst`;
the encoder now writes through a pooled `scratch.AppendWriter` over `dst`, so
the copy is gone. Every allocation left is `encoding/xml`'s own:
`xml.NewEncoder` takes a 4 KiB `bufio.Writer` and its printer state on each
call, and has no `Reset` to reuse one. `Marshal` allocates one more, its
result.
