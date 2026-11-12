<!-- generated from internal/kernel/fs/seglog/seglog_bench_test.go — run `cd internal && go test -run='^$' -bench=. -benchmem -cpu 1 -count 5 ./kernel/fs/seglog/` to refresh -->
# Benchmarks — `internal/kernel/fs/seglog`

The segmented log extracted from the consensus file log (#820, ADR 0230):
one batch of 1, 16 and 256 records of 128 bytes appended and made durable
by `Sync`, and 256 durable records read back with one `Range`. Every
segment is `DefaultSegmentBytes` (64 MiB), the next one zero-filled in the
background, so a rotation falls inside the measured loop.

## Reproducibility envelope

| Dimension | Linux (primary) | macOS |
|---|---|---|
| CPU | Apple M1 Pro, 8 vCPU given to the VM | Apple M1 Pro (10 cores) |
| RAM | 7.7 GiB (VM) | 16 GiB |
| OS / kernel | Linux 7.0.14-linuxkit (Docker Desktop VM), overlay on the VM's virtual disk | macOS 26.6.2, APFS |
| Architecture | arm64 | arm64 |
| Go toolchain | go1.27.3 linux/arm64 (`golang:1.27.3` image) | go1.27.3 darwin/arm64 |
| Durability call | `fdatasync(2)` | `fsync` + `F_FULLFSYNC` (`os.File.Sync`) |
| Git base | `b4c299a46` + #820 | `b4c299a46` + #820 |
| Generated | 2026-11-12 | 2026-11-12 |
| Bench wall-clock | `-benchmem -count 5 -cpu 1`, median of 5 runs | same |

Both runs shared the machine with seven other builds: the spread between runs
is wide (the five runs of each row span up to 2×), and the medians are what
the table reports. The Linux disk is a virtual one, so its sync latency is
the hypervisor's, not a device's; a bare-metal NVMe run is the number to
compare a regression against.

## Results

| Benchmark | Linux ns/op | Linux ns/record | macOS ns/op | macOS ns/record | B/op (Linux) | allocs/op |
|---|---|---|---|---|---|---|
| `AppendSync/records=1` | 469 693 | 469 693 | 14 823 640 | 14 823 640 | 474 | 0 |
| `AppendSync/records=16` | 1 067 267 | 66 704 | 22 850 007 | 1 428 125 | 2 683 | 0 |
| `AppendSync/records=256` | 1 445 992 | 5 648 | 20 332 561 | 79 424 | 33 959 | 0 |
| `Range` (256 × 128 B) | 38 382 | — | 68 848 | — | 40 960 | 1 |

## How to read this

- **`AppendSync`** — the cost of a batch is its syncs, not its records. A
  batch of one record is ONE sync (there is no body before its end record); a
  batch of several is TWO — the body, then the end record alone (the
  two-step protocol the consensus log adopted in #417) — so 16 records cost
  about two to three times one record, and 256 records little more than 16.
  Per record, batching 256 is ~80× cheaper than syncing each one on Linux.
  On macOS every sync is `F_FULLFSYNC`, a flush of the device's cache, which
  is why each row is tens of milliseconds and the row is dominated by the
  device, not by the record count: the durability macOS gives is real there
  and costs that.
- **allocs/op is 0** on the append path: the pending batch is a buffer
  reused from batch to batch, and the B/op shown is its growth and the
  rotations' zero-fill buffer, amortised over the iterations.
- **`Range`** — one `pread` for the 256 contiguous records and one CRC-32C
  check per record; the one allocation is the run's buffer, which the 256
  payloads share (each clipped to its own capacity).
