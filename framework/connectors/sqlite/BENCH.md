<!-- generated from framework/connectors/sqlite/sqlite_bench_test.go — run `cd framework/connectors/sqlite && GOWORK=off go test -run='^$' -bench=ACommit -benchmem -count=5 .` to refresh -->
# Benchmarks — `framework/connectors/sqlite`

**The conclusion first: on this box a commit under the engine's
`synchronous=NORMAL` costs about 72 µs, against about 1.77 ms under SQLite's
own default `FULL` — some 24 times less — and the same 23 allocations.** The
difference is the fsync of the WAL that FULL pays at every commit and NORMAL
pays at the checkpoint only (#999). It is the disk's number, not the
engine's: on a disk whose fsync is cheaper the ratio shrinks, and the
allocations are the driver's and database/sql's on both sides.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU                | AMD EPYC 7351P 16-Core, 8 vCPU visible (run with GOMAXPROCS=4) |
| RAM                | 15 GiB |
| OS / kernel        | Linux 6.12.101+deb13-amd64 (Debian GNU/Linux 13, trixie) |
| Disk               | the test's `b.TempDir()`, ext4 on a virtual disk |
| Architecture       | amd64 |
| Go toolchain       | go1.27.3 linux/amd64 |
| Git commit         | the L6 data-lot branch over `ee1f48ba` (pre-commit) |
| Date               | 2026-11-02 |
| Bench wall-clock   | `-benchtime=1s -count=5`, median quoted |

## Results

| Benchmark | ns/op (median) | B/op | allocs/op |
|---|---:|---:|---:|
| `ACommitUnderFullAndNormal/FULL` — BEGIN, one INSERT, COMMIT, `_synchronous=FULL` | 1 768 452 | 988 | 23 |
| `ACommitUnderFullAndNormal/NORMAL` — the same, the engine's default `NORMAL` | 72 016 | 984 | 23 |

## Reading them

- The transaction is the smallest a store's `Put` runs in: a write
  transaction (`_txlock=immediate`), one row, a commit. A store's own
  encoding and its index rows add the same work under both modes, so the
  difference measured here is what a store's write gains.
- NORMAL under WAL is the mode SQLite documents as safe from corruption: a
  power cut may lose the commits since the last checkpoint, never the
  database's consistency. A product that wants every commit on disk writes
  `?_synchronous=FULL` in its URL, which the engine never overrides.
- `journal_size_limit` (64 MiB) costs nothing a commit can see: it truncates
  the WAL after a checkpoint, which this loop does not reach.

## Gates

`sqlite_bench_test.go` carries no build tag and no `gazelle:excluded`: it is
compiled by every `go test` of the module and by `make test-framework`
(CLAUDE.md rule 12 — nothing here is hidden from normal discovery).
