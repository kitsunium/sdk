<!-- generated from internal/service/data/codec/jcs/jcs_bench_test.go — run `go test -run='^$' -bench=. -benchmem -benchtime=300ms -count=6 ./internal/service/data/codec/jcs/` to refresh -->
# Benchmarks — `internal/service/data/codec/jcs`

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
| `Canonicalize8KB` (8 182 bytes: 60 objects of five members, nested arrays) | 272 100 (≈ 21 MB/s) | 87 510 | 1 043 |
| `MarshalJWKMembers` (four members from a map, through json/v2) | 4 691 | 858 | 15 |
| `CanonicalizeJWKMembers` (the same four, already encoded) | 2 581 | 768 | 11 |

The time is `jsontext`'s token reader (a quarter of the profile) and the
canonical writer's own work: each member name becomes a Go string to be
sorted, each object canonicalizes its values into a buffer of its own, then
sorts and copies them out. About one allocation per member and per string
value on the 8 KB document. A first version built a `jsontext.Decoder` per
call: on the four JWK members it allocated 33 times instead of 22 for the
whole thumbprint — the decoder is pooled since.
