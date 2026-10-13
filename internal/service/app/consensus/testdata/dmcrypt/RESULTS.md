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
