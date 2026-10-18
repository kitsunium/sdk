<!-- updated: 2026-10-18T12:00:00Z -->
# internal/service/app/consensus/

## Purpose

The Raft engine of ADR 0152 and everything it ships: the event loop over the
step function (`raft/`), the storage actor, the apply goroutine, snapshots
in checksummed chunks, the segmented file log and its recovery procedure,
the file snapshot store, the data directory with its lock, stamp and stop
markers, the offline operations (`Init`, `Recover`, `RestoreSnapshot`,
`Inspect`, `ClearQuarantine`), the mTLS TCP transport over `net/tlsid` and
`net/server.Admission` (ADR 0174), and the in-memory doubles. It declares no
code: it raises the core's.

## Lanes

| Lane | How |
|---|---|
| unit, cluster, disk, TCP | `go test -race ./...` |
| simulator soak | `CONSENSUS_SIM_SOAK=1 go test -run TestSimSoak ./raft/`; a seed replays with `CONSENSUS_SIM_SEED` |
| fuzz | `go test -fuzz` on the targets of `wire/` and this package; nightly in CI |
| dm-crypt | `testdata/dmcrypt/run.sh` (Docker, privileged): fsync latency and power cuts through LUKS; results in `testdata/dmcrypt/RESULTS.md` |

## Do NOT

- Let the event loop wait on storage or a socket (D5b).
- Retry a failed sync: the node stops (D4).
- Truncate a hole before every segment is classified (R8-02, R9-03).
- Seal a segment again in recovery: a durable seal only needs the next
  segment published (`publishNext`, review H2).
- Restore a snapshot the machine has applied past, or install one whose
  freshness was not checked again after the transfer (review H3).
- Hold `mu` across a batch's write and fdatasync: writers serialise on
  `wmu`, readers must not wait for the disk (review L6).
- Take a JOIN answer from an unpinned peer (`JoinConfig.Pins`, review M4),
  or admit a key two members share (review M5).
- Report a refused transfer as done on the node's own view that the target
  leads: a deposed leader that has not heard its successor still names
  itself. `confirmLeader` makes the view a fact with a barrier first.
- Publish a snapshot without `retain` after it: a cut and an install both
  keep the two newest (D7, R6-02). An install used to skip it and left three
  until the node's next cut (#304, `TestAnInstallKeepsTheTwoNewestSnapshots`).
- Dial a sender link (control or data lane) without `boundUnacknowledged`,
  or drop the batch a reused connection failed: a link quiet since a
  partition keeps taking small writes into a stream the kernel retransmits
  under exponential backoff, and an election waited 15 s for it
  (`TestTCPFrameOnALinkThatDiedQuietlyIsDelivered`). The snapshot and JOIN
  lanes stay unbounded: a receiver writing a snapshot may stop reading.
- Drop a snapshot trigger that falls while a cut is in flight: D7 coalesces
  it (`snapState.pending`, `cutPending`), and the cut is made once the one in
  flight is written. Dropped, a node that applied nothing more kept its tail
  uncut and its log uncompacted (#375, `TestACoalescedCutIsMade`). A write
  that fails clears the coalesced trigger with the in-flight flag
  (`TestAFailedCutClearsTheCoalescedTrigger`).
- Let a stopped node of a memory-network test hear what the cluster sent
  while it was down: the network keeps those frames and delivers them at the
  restart, which catches the node up by appends where the test meant an
  install. Isolate it before it stops (#374,
  `TestInstallResumesAfterACrashAtPublication`).
