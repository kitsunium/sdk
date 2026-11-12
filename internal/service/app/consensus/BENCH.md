<!-- generated from internal/service/app/consensus/filelog_bench_test.go — run `cd internal && go test -run '^$' -bench BenchmarkFileLogEntries -benchmem -count=3 ./service/app/consensus/` to refresh -->
# Benchmarks — `internal/service/app/consensus`

One question, the read side of ADR 0152 D15's "segmented log" row (#913):

> **What does a fetch of synced entries cost per entry, now that a run of one
> segment is read with one pread and parsed in place?**

Once a batch is synced, every read of its entries — a slow follower's fetch,
the apply goroutine's read back past `RecentEntriesBytes`, `Recover` and
`Inspect` — goes to the segment file. `per-entry` is the path before #913:
`readEntry`, a pread of the header then a pread of the record into a buffer of
its own, two allocations an entry. `range` is `Entries` since #913: the run's
interval, known from the slots, read with ONE pread into one buffer, each
entry's `Data` aliasing it. The 4 096-entry row is the issue's 1 MiB fetch of
256-byte entries.

## Reproducibility envelope

> **Numbers vary across machines, and these are noisy.** They were taken while
> seven other test lanes ran on the same machine, and the page cache held the
> segment: the ns/entry columns spread by up to 2x between runs of one row.
> What travels is the shape — the syscall and allocation counts are exact,
> and the order of magnitude between the two paths holds in every run.

| Dimension | Value |
|---|---|
| CPU | Apple M1 Pro, 10 cores |
| RAM | 16 GiB |
| OS | macOS 26.6.2 (25G83), darwin/arm64 |
| Go | go1.27.3 |
| Commit | b4c299a46 plus the change of #913 |
| Taken | 2026-11-11, `-count=3`, default `-benchtime` |

## Results

| Benchmark | ns/op (3 runs) | ns/entry | B/op | allocs/op | preads |
|---|---|---|---|---|---|
| `range/64` | 78 712, 72 733, 46 046 | 1 230, 1 137, 720 | 20 480 | 1 | 1 |
| `per-entry/64` | 187 137, 306 964, 453 618 | 2 924, 4 796, 7 090 | 20 992 | 128 | 128 |
| `range/4096` | 1 514 128, 2 145 751, 2 076 112 | 371, 524, 507 | 1 278 456 | 1 | 1 |
| `per-entry/4096` | 35 506 749, 26 240 787, 28 013 003 | 8 693, 6 798, 7 163 | 1 343 514 | 8 192 | 8 192 |

The preads column is counted, not timed: `TestEntriesReadASyncedRunOfOneSegmentInOnePread`
and the alloc lane's `TestEntriesOfASyncedRunAllocateTheBufferAndTheSlice`
assert it. `range` passes a reused `dst`, so its one allocation is the read's
buffer; into a nil `dst` the slice is grown once more, two allocations, which
is D15's budget (`Entries of 64 x 256 B: 1 pread, <= 2 allocs/op`).

## Reading it

- **The 1 MiB fetch** went from 8 192 syscalls and 8 192 allocations to one
  of each, and from 7 to 9 µs an entry to 0.4 to 0.5 µs: 13 to 23 times
  faster in these runs.
- **64 entries** gain less per entry (the one pread's fixed cost is spread
  over fewer records) and still 2.5 to 10 times.
- **The bytes are the same**: one buffer of the records' aligned length
  against a buffer per record; the 512 B more of `per-entry/64` are its 64
  headers.

## Preallocation of a segment (G1, #922)

ADR 0152 G1's row `Append 64 x 256 B + Sync`, in both modes of
`DataDirOptions.Preallocate`, is measured per filesystem by the dm-crypt
lane, not by `go test -bench`: it needs Linux, a block device whose bytes it
can count and a filesystem it formats (`testdata/dmcrypt/run.sh 12 0 <ext4|xfs>
fallocate`, `TestDMCryptPreallocBench`). 64 MiB segments, filled until three
more were published, the modes alternated so the host's load does not land on
one only; "device bytes" is what the dm device was sent per segment, journal
included, against 67.1 MB of records. The lane's raw lines are in
`testdata/dmcrypt/RESULTS.md`.

| Filesystem | Mode | syncs | `Sync` p50 | p90 | p99 | device bytes per segment |
|---|---|---|---|---|---|---|
| ext4 | ZeroFill | 10 081 | 372 µs | 2.46 ms | 16.5 ms | 161 842 517 |
| ext4 | Fallocate | 10 081 | 990 µs | 5.05 ms | 18.0 ms | 141 275 136 |
| ext4 | ZeroFill | 10 081 | 636 µs | 6.16 ms | 26.9 ms | 161 839 786 |
| xfs | both | — | not measured | | | |

Taken 2026-11-12 at a9714f4b3 (#1110) plus this change, Docker Desktop's
Linux VM on an Apple-silicon Mac (debian:stable-slim, LUKS2 aes-xts-plain64
over a loop device on a tmpfs), under the load of seven other builds. The
run stopped there: the host's disk filled, and xfs and the fourth row were
not reached.

- **ext4**: `fallocate` writes 13 % fewer bytes, not the 64 MiB of zeros,
  since each `fdatasync` after a write into an unwritten extent journals its
  conversion, and the median `Sync` is 1.6 to 2.7 times the zero-filled
  one's. Not adopted: ZeroFill stays the default (ADR 0152, Deferred).

## Sync path of a batch (G1, #923)

ADR 0152 G1's row `Append 64 x 256 B + Sync`, `DataDirOptions.SyncPath =
SyncRWFDsync` (one `pwritev2(2)` flagged `RWF_DSYNC` per durability point)
against `SyncFdatasync` (`pwrite(2)` then `fdatasync(2)`), is measured per
device by the dm-crypt lane in ADR 0190's shape: both logs in one process on
the same filesystem, a round of 2 x 8 windows of 200 Syncs in the order A, B,
B, A, ..., ten rounds, and the gate held on the distribution-free interval of
the ten ratios B / A (2nd and 9th order statistics):
`testdata/dmcrypt/run.sh 12 0 <ext4|xfs> zerofill rwfdsync`,
`TestDMCryptSyncPathBench`, its `SYNCPATH` and `SYNCPATH_GATE` lines. A mode
may become the default on a device only if its entries/s ratio's lower bound
is >= 1.00 and its Sync p99 ratio's upper bound is <= 1.00.

| Device | Filesystem | ratio entries/s | ratio Sync p99 | verdict |
|---|---|---|---|---|
| dm-crypt over a loop device | ext4 | not measured | not measured | Fdatasync stays the default |
| dm-crypt over a loop device | xfs | not measured | not measured | Fdatasync stays the default |
| OVH VPS of D16 | — | not measured | not measured | Fdatasync stays the default |

Not measured on 2026-11-12: the change was written on a macOS host whose
Docker could not start and whose disk was full, so the lane did not run.
`SyncFdatasync` stays the default everywhere until a row meets the gate.
