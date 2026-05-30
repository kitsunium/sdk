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
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.88+deb13-amd64 |
| Architecture       | amd64 |
| Go toolchain       | go1.26.3 linux/amd64 |
| Git branch         | feat/writer-subsystem-v2 |
| Git commit         | (current HEAD, pre-commit) |
| Generated (UTC)    | 2026-05-30 |
| Bench wall-clock   | `-test.benchtime=1s`, single run |

## Results

```
BenchmarkStreamSeal_1MiB-12   	471	2540641 ns/op	 412.72 MB/s	3533269 B/op	66 allocs/op
BenchmarkStreamOpen_1MiB-12   	726	1644532 ns/op	 637.55 MB/s	3554049 B/op	36 allocs/op
```

## How to read this

- **Throughput** (`MB/s`) is the headline number for a streaming AEAD: ~400 MB/s
  seal, ~640 MB/s open on this box, dominated by hardware AES-NI + the SHA-256
  HKDF key-derivation done once per stream.
- **allocs/op** reflects the per-1-MiB stream cost: the 16 chunks each allocate a
  fresh sealed/opened slice plus the buffered plaintext. The construction favours
  bounded memory (two chunk-sized buffers) over zero-alloc — the whole point of
  streaming is to avoid holding the full payload twice.
- Numbers are a single-run snapshot; treat as ±5%. Re-run on a representative
  benchmark machine before citing in a release.
