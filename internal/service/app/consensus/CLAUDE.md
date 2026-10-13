<!-- updated: 2026-10-13T12:00:00Z -->
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
