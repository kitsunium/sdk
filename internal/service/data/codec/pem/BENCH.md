<!-- generated from internal/service/data/codec/pem/append_bench_test.go — run `go test -run='^$' -bench='Marshal1KiB|Append1KiB' -benchmem -benchtime=500ms -count=6 ./internal/service/data/codec/pem/` to refresh -->
# Benchmarks — `internal/service/data/codec/pem`

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

The value is a `CERTIFICATE` block of 1 KiB (`kibValue`).

| benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Marshal1KiB` | 3 293 → 1 902 (−42 %) | 2 832 → 1 536 | 5 → **1** |
| `Append1KiB` | 3 351 → 1 404 (−58 %) | 2 832 → **0** | 5 → **0** |

Before, every block but the promotion shape went through `encoding/pem.Encode`
(a `base64.NewEncoder` and its line breaker over a pooled buffer) and a clone,
and `Append` added a copy. The block is now written by the package's own
encoder (`appendBlock`: one base64 encode into the destination, then the
64-character lines moved into place, the headers ordered as `encoding/pem`
orders them), pinned against `encoding/pem.EncodeToMemory` by
`TestAppendBlockIsEncodingPEM` and `FuzzAppendBlockIsEncodingPEM`. `Marshal`
allocates its result once at its exact size, `Append` nothing when `dst` has
the room.
