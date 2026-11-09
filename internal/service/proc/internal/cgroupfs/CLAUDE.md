<!-- updated: 2026-11-04T00:00:00Z -->
# internal/service/proc/internal/cgroupfs/

## Purpose

The **one reading of where this process sits in the control-group filesystem**
the proc family uses: every mount of the unified hierarchy and of the v1 memory
controller with the part of the cgroup filesystem each exposes (`Mounts`,
mountinfo's octal escapes decoded), the process's membership in each
(`Membership`), the translation of a membership path under a mount that exposes
a subtree (`UnderMountRoot`), and the directory of its own unified cgroup
(`OwnGroup`). Every function takes the file reader, so a test drives each shape
without a cgroup filesystem.

It was `proc/memlimit`'s `cgroup.go` until #1097 (`cgroup.Probe` names the
caller's own group) and #938 (`self`'s container id) needed the same reading.
One reading is not worth an edge from one engine to another, so it moved here,
under `internal/`, where Go's rule confines it to the proc family — as
`internal/rlim` did for the rlimit constructor. The why of each rule (a mount
point is not a mount root; every attachment is kept; mountinfo is escaped and
`/proc/self/cgroup` is not) is recorded in `proc/memlimit/CLAUDE.md`, where it
was learned, and holds here unchanged.

Code range: **none** — it reads files and returns values, never an error.

Importers: `proc/memlimit`, `proc/cgroup` (Linux), `proc/self`.

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `cgroupfs.go` | `SelfCgroupFile`, `SelfMountinfoFile`, `MountValue`, `Membership`, `UnderMountRoot`, `Mounts`, `OwnGroup`, and the mountinfo field parser and unmangler |
| `cgroupfs_internal_test.go` | the parser tables moved from `memlimit` with the code, and `TestOwnGroup` |

## Do NOT

- Read a limit file here: which files bound a process is `memlimit`'s question.
- Import an engine of the family: this package sits below all of them.

## Verification

```sh
go test -race ./internal/service/proc/internal/cgroupfs/
```
