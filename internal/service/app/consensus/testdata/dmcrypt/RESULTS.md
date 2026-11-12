# dm-crypt lane — results (ADR 0152 D15, D16, R9-03)

Run with `testdata/dmcrypt/run.sh 30` and `run.sh 10 1` on 2026-10-13.

**Where it ran.** Docker Desktop 29.8.2 on an Apple-silicon Mac: a
privileged `debian:stable-slim` container in the Linux VM (kernel
7.0.14-linuxkit, aarch64). Stack: a sparse 8 GiB file → loop device → LUKS2
`aes-xts-plain64`, 512-bit key → a dm-linear device (the "cut" device) →
ext4. The VM's disk is the Mac's: these numbers say nothing about the OVH
VPS. D16 keeps its targets provisional until `fdatasync` is measured on the
three VPS under their real load.

## fsync per batch (one `Sync` per batch, segments of 64 MiB)

| Shape | n | p50 | p90 | p99 | max | entries/s |
|---|---|---|---|---|---|---|
| 64 × 256 B (D15's row) | 2000 | 188 µs | 502 µs | 2.69 ms | 45.3 ms | 156 605 |
| 1 × 256 B | 2000 | 148 µs | 552 µs | 2.93 ms | 19.7 ms | 3 077 |

Two earlier runs on the same machine gave p99 0.88 ms and 4.52 ms for the
64 × 256 B shape: the tail moves with the host's load. D16's "≥ 10 000
entries/s of 256 B with one fsync per batch" holds here by a factor of 15 at
64 entries per batch; one entry per batch is fsync-bound, as expected.

## Power cut through dm-crypt (R9-03)

A writer appends batches of 1 to 32 entries (100 B to 3 KB, contents derived
from the index), syncs each, and prints the last index only once `Sync`
returned. At a random instant the cut device's table is swapped for an
`error` target (`dmsetup suspend --noflush`, `load`, `resume`): every write
not yet on the crypt device is lost, as in a power cut. The writer is
killed, the filesystem dropped with its caches, the linear table restored,
ext4 remounted (journal replay), and the log reopened. It must not be
quarantined, must hold every acknowledged entry, each byte-identical.

| Run | Iterations | Passed | Torn tails truncated | Acknowledged lost |
|---|---|---|---|---|
| safe writer | 30 | 30 | 1 per reopen | 0 |
| negative control: acknowledged BEFORE sync | 10 | 0 (all caught) | — | in every iteration |

The log grew to 372 239 entries over the 30 cuts on one directory. In one
iteration 16 entries past the last acknowledgement survived: a batch whose
sync returned before the cut but whose acknowledgement line did not.

## Cut after a rotation's seal (review of PR #301, H2)

Since 2026-10-13 the writer uses 256 KiB segments, so cuts often land near a
rotation. Every third iteration stops the writer right after a seal is
durable and before the next segment is published, then cuts there. The
verifier opens the log twice: the first open finishes the rotation, and the
second must accept what the first one wrote.

| Run | Iterations | At a seal | Passed |
|---|---|---|---|
| fixed log (`publishNext` after a durable seal) | 9 | 3 | 9 |
| control: recovery seals again (`rotate(0)`, the bug) | 3 | 1 | 1 (2 quarantined at the second open) |

The control quarantined at a random cut too: with small segments a cut in
that window is common.

## Preallocation, ZeroFill against Fallocate (#922)

Run with `DMCRYPT_RAM=1 run.sh 12 0 ext4 fallocate` on 2026-11-12, the loop
device's image on a 3 GiB tmpfs, under the load of seven other builds. The
run stopped after the third `PREALLOC` line: the host's disk filled and
Docker's VM went read-only, so the fourth line, the power cuts and the file
log's suite on fallocated segments were not reached, nor the xfs run.

```
SETUP fs=ext4 preallocate=fallocate  cipher: aes-xts-plain64  keysize: 512 bits
BENCH batch64x256B n=2000 p50=2.329917ms p90=11.12175ms p99=27.704917ms max=70.138833ms entries_per_s=13027
BENCH batch1x256B n=2000 p50=92.208µs p90=391.083µs p99=6.562167ms max=27.051708ms entries_per_s=3221
PREALLOC fs=ext4 mode=zerofill syncs=10081 segments=3 p50=371µs p90=2.463833ms p99=16.465833ms max=60.493708ms device_bytes_per_segment=161842517 record_bytes_per_segment=67099136
PREALLOC fs=ext4 mode=fallocate syncs=10081 segments=3 p50=989.833µs p90=5.048666ms p99=17.996167ms max=52.192083ms device_bytes_per_segment=141275136 record_bytes_per_segment=67099136
PREALLOC fs=ext4 mode=zerofill syncs=10081 segments=3 p50=635.917µs p90=6.157792ms p99=26.888125ms max=2.418100168s device_bytes_per_segment=161839786 record_bytes_per_segment=67099136
```

The verdict is ADR 0152's (Deferred) and the package's `BENCH.md`.

### On Linux: ext4 and xfs (devbox, 2026-11-13)

`run.sh 12 0 ext4 fallocate` and `run.sh 12 0 xfs fallocate` on devbox (Debian
13, kernel 6.12.101 amd64, 8 cores): a privileged `debian:stable-slim`
container, LUKS2 `aes-xts-plain64` over a 4 GiB loop image kept on the
host's ext4 disk, the modes alternated twice. `SUITE ok` is the file log's
suite and the fuzz seeds of `FuzzParseRecord`, `FuzzClassifyTail` and
`FuzzScanSegment`, every log fallocated; the `RESULT` line is 12 power cuts.

```
PREALLOC fs=ext4 mode=zerofill syncs=10081 segments=3 p50=2.283939ms p90=2.688322ms p99=3.818636ms max=201.685696ms device_bytes_per_segment=161858901 record_bytes_per_segment=67099136
PREALLOC fs=ext4 mode=fallocate syncs=10081 segments=3 p50=3.712287ms p90=4.765416ms p99=7.269796ms max=33.363524ms device_bytes_per_segment=141186389 record_bytes_per_segment=67099136
PREALLOC fs=ext4 mode=zerofill syncs=10081 segments=3 p50=2.345554ms p90=2.847608ms p99=5.689334ms max=133.526123ms device_bytes_per_segment=161850709 record_bytes_per_segment=67099136
PREALLOC fs=ext4 mode=fallocate syncs=10081 segments=3 p50=4.280314ms p90=8.651346ms p99=13.260672ms max=161.118138ms device_bytes_per_segment=141190485 record_bytes_per_segment=67099136
SUITE ok fs=ext4 preallocate=fallocate sync_path=fdatasync passed=41 skipped=1
RESULT crash_iterations=12 passed=12 negative_control=0 fs=ext4 preallocate=fallocate sync_path=fdatasync
PREALLOC fs=xfs mode=zerofill syncs=10081 segments=3 p50=2.463985ms p90=3.351996ms p99=9.186111ms max=161.724657ms device_bytes_per_segment=161819306 record_bytes_per_segment=67099136
PREALLOC fs=xfs mode=fallocate syncs=10081 segments=3 p50=4.413011ms p90=6.934592ms p99=12.515646ms max=70.377491ms device_bytes_per_segment=100507306 record_bytes_per_segment=67099136
PREALLOC fs=xfs mode=zerofill syncs=10081 segments=3 p50=2.414292ms p90=3.11734ms p99=9.784405ms max=373.486208ms device_bytes_per_segment=161752064 record_bytes_per_segment=67099136
PREALLOC fs=xfs mode=fallocate syncs=10081 segments=3 p50=4.418561ms p90=9.096665ms p99=14.058688ms max=131.037711ms device_bytes_per_segment=100498602 record_bytes_per_segment=67099136
SUITE ok fs=xfs preallocate=fallocate sync_path=fdatasync passed=41 skipped=1
RESULT crash_iterations=12 passed=12 negative_control=0 fs=xfs preallocate=fallocate sync_path=fdatasync
```

`fallocate` writes 13 % fewer bytes on ext4 and 38 % fewer on xfs, and its
median `Sync` is 1.6 to 1.8 times ZeroFill's on both. Not adopted: ZeroFill
stays the default on ext4 and xfs.
