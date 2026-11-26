<!-- updated: 2026-11-08T12:00:00Z -->
# internal/service/proc/cgroup/

> Reference moved to `internal/service/proc/cgroup/NOTES.md` (ADR 0253), under the same section titles: Performance — see BENCH.md.

## Purpose

Creates and manages **cgroup v2** control groups to confine a process tree's
memory / CPU / pids / IO, for the OS process-supervision domain (ADR 0016). It
implements the `core/proc.Group` port against the unified cgroup v2 hierarchy at
`/sys/fs/cgroup`. **Stdlib-only** (`os`, `path/filepath`, `strconv`, `strings`,
`errors`, `syscall`, and `sync` / `unsafe` in the Windows and FreeBSD backends)
plus `internal/core/proc` and `internal/kernel/errs` — no `golang.org/x/sys`.

## Contents

| File | Role |
|---|---|
| `cgroup.go` | platform-neutral surface: `Available`, `Create`, and `Probe` + `ProbeValue` (#1097) delegating to the build-tagged impls; Linux's `probe` reads WithRoot's root (the `cgroup.controllers` marker and list, the same create-and-remove delegation test `available` runs, now `delegated`) and names the caller's own cgroup through `internal/cgroupfs`; Windows and FreeBSD answer Mounted = Delegated = `available()`, no own group; the stub answers `UnsupportedPlatform` |
| `option.go` | `Option` functional-option type, `WithRoot`, `groupConfig` accumulator |
| `cgroup_linux.go` | Linux impl: `controlGroup` (satisfies `core/proc.Group`), controller writes, `validateName` (a nested name of literal elements), `createGroup` (through `inTree`), rmdir |
| `tree_linux.go` | #727: `inTree` (an `os.Root` on the root, so nothing resolves outside it), `makeTree` / `makeStep` / `unmake` (the missing groups made top-down, a link refused, undone on failure), `enableIn` / `rootOffers` / `validateControllers` (`cgroup.subtree_control`), and `GroupTree` — `Path`, `EnableControllers`, `Delegate` (`/sys/kernel/cgroup/delegate`'s list, the three documented files without it), `DeleteTree` (`subtreeOf`, parents first, removed in reverse; no link followed) |
| `tune_linux.go` | #727: `GroupTuner` — `memory.min/low/high/swap.max/zswap.max/zswap.writeback/oom.group`, `cpu.weight`, `cpu.max.burst`, `cpuset.cpus/mems` (`listOf`, the kernel's `0-3,8`), `io.weight` (`default N`), `io.latency` (`MAJ:MIN target=USEC`, `max` to remove) |
| `watch_linux.go` | #727: `GroupWatcher.Watch` — one inotify descriptor on the runtime's poller (a past read deadline is the cancel), `cgroup.events` + `memory.events` + the parent directory, the latest state in a one-slot channel |
| `membership_linux.go`, `membership_other.go` | S0g: `Membership` — a group's directory as `/proc/<pid>/cgroup` names it, off the cgroup2 mount that holds it; `UnsupportedPlatform` elsewhere |
| `members_linux.go` | #727: `GroupSignaler` — `Signal` (`signalTree`: passes over `cgroup.procs` of the subtree until none is new, 64 at most, never a pid ≤ 0 nor the caller) and `Contains`; `procsOf`, `treeMembers` |
| `groupstat_linux_compliance.go` | the Linux group's five sibling ports asserted (#932, #727) |
| `cgroup_windows.go` | Windows impl via **Job Objects** (kernel32 `CreateJobObjectW` / `SetInformationJobObject` / `AssignProcessToJobObject` / `TerminateJobObject`); memory/CPU/pids caps; IO + Freeze/Thaw → `UnsupportedPlatform`; `JobLimiter.SetJobMemoryLimit` — `JOB_OBJECT_LIMIT_JOB_MEMORY`, the whole job's cap beside `SetMemoryMax`'s per-process one (#727) |
| `cgroup_freebsd.go` | FreeBSD impl via **rctl** (`rctl_add_rule`/`rctl_remove_rule` syscalls); per-process `vmemoryuse`/`pcpu` rules (memory/CPU caps); pids/IO + Freeze/Thaw → `UnsupportedPlatform` |
| `cgroup_other.go` | `!linux && !windows && !freebsd` stub (darwin + remaining BSDs): `Available() == false`, `Create` returns `UnsupportedPlatform` |

No `codes.go` / `errors.go` — every error is a `core/proc` sentinel
(`CgroupUnavailable`, `CgroupCreateFailed`, `CgroupWriteFailed`,
`CgroupDeleteFailed`, `CgroupReadFailed`, `CgroupControllerAbsent` —
0.2.6.37, #727 —, `InvalidSpec`, `SignalFailed`, `UnsupportedPlatform`); this
package mints no codes.

## Behaviour

- **Available()** — honest delegation probe: the `cgroup.controllers` marker
  must exist under the mount **and** a throwaway sub-directory must be creatable
  (a read-only root, the common unprivileged-container case, reports `false`).
- **Create(name, opts...)** — `mkdir` a sub-group under the configured root
  (default `/sys/fs/cgroup`, override via `WithRoot` for a delegated sub-tree).
  An absent hierarchy or a delegation denial (EACCES/EPERM/**EROFS**) →
  `CgroupUnavailable`; any other `mkdir` fault → `CgroupCreateFailed`.
  Since #727 `name` may be nested (`"a/b/c"`): every element is a literal
  name — empty, absolute, non-canonical, `.` or `..` is `InvalidSpec` —, the
  missing groups are made top-down through an `os.Root` opened on the root
  (the kernel's `openat2` walk refuses a `..` or a link out of it), an
  existing element that is not a directory — a link, even one inside the
  root — is `InvalidSpec`, the last group must be new, and what the call made
  is removed again when a later step fails. `WithControllers(ctrls...)`
  checks them against the root's `cgroup.controllers`
  (`CgroupControllerAbsent` before anything is made, a malformed name
  `InvalidSpec`), then writes `+a +b` to the `cgroup.subtree_control` of the
  root and of every group on the way, never of the new group itself: the
  agent's `cgroup_delegation_linux.go` moves here, the SDK the only writer.
  A controller enabled stays enabled when a later step fails (enabling is
  idempotent, and a sibling may already rely on it); only the groups the
  call made are undone.
- **Set\*Max** — write the cgroup v2 interface file: `memory.max`, `cpu.max`
  (`"quota period"`), `pids.max`, one `io.max` line verbatim. A **negative**
  numeric value writes the literal `"max"` (no limit). Failure →
  `CgroupWriteFailed`.
- **Add(pid)** — write `pid` to `cgroup.procs`. Failure → `CgroupWriteFailed`.
- **Kill()** — write `"1"` to `cgroup.kill` (kernel ≥ 5.14): atomic, race-free
  SIGKILL of every member, **including `setsid` escapees** that `SignalGroup`
  misses. An absent feature file (older kernel) → `UnsupportedPlatform` so the
  caller can fall back; any other write fault → `CgroupWriteFailed`. **Kill a
  group you will not spawn into again**: measured on Linux 6.12.101, after one
  `cgroup.kill` — an empty group's included — every child later cloned into
  the group (`Spec.CgroupPath`, CLONE_INTO_CGROUP) is SIGKILLed at its fork,
  while a process migrated in is not. `proc/service` therefore kills a unit's
  group through `Signal(SIGKILL)` (ADR 0214's S0g amendment, §6).
- **Freeze() / Thaw()** — write `"1"` / `"0"` to `cgroup.freeze` (kernel ≥ 5.2):
  quiesce/resume the whole tree (idempotent). Absent file → `UnsupportedPlatform`.
- **Delete()** — `rmdir` the group; the kernel refuses (EBUSY) until every
  process has left it → `CgroupDeleteFailed`.

- **The Linux group's siblings (#727, ADR 0039)** — asserted on the `Group`
  `Create` returns, Linux only (Windows and FreeBSD have none of them;
  Windows has `JobLimiter`):
  - `GroupTuner` — thirteen typed setters; a byte value below zero writes
    `max`, a weight outside 1–10000, a negative burst, CPU or latency is
    `InvalidSpec` with nothing written, and a file whose controller the
    parent did not enable is `CgroupWriteFailed` (absent file).
  - `GroupTree` — `Path` (what `Spec.CgroupPath` takes: S0g spawns into it),
    `EnableControllers` (the group's own `cgroup.subtree_control`; EBUSY while
    the group holds a process — the no-internal-process rule — is
    `CgroupWriteFailed`), `Delegate(uid, gid)` (`lchown` of the directory and
    of the kernel's delegation list, a missing file skipped), `DeleteTree`
    (the group and what a delegated unit made beneath it, deepest first; a
    populated group stops it with `CgroupDeleteFailed` naming it — `Delete`
    still removes the leaf alone). `DeleteBeneath` (S0g, #734; a method of the
    Linux group, `proc/service`'s `CgroupUnit` names it) removes what lies
    beneath and keeps the group: a `Delegate=yes` unit's cleanup at each end
    of a run, the unit's group being its caller's.
  - `GroupWatcher.Watch(ctx)` — the first state, then each change, in a
    one-slot channel where a burst coalesces into the latest (the watch never
    blocks on its reader). A read that follows an `IN_MODIFY` of
    `cgroup.events` or `memory.events` is published even when it equals the
    last value — a burst that changed the state back, populated 0, 1, 0
    between two reads, is still news to a reader that read the group
    meanwhile (S0g); a record that announces no change of the group — a
    sibling's removal in the parent — publishes nothing. **Removal
    on cgroupfs is the parent's `IN_DELETE`**: an `rmdir` of a group raises no
    `IN_IGNORED` for a watch on its files (measured, Linux 6.12 — the files go
    inside kernfs, not through the VFS), so the parent directory is watched
    for the group's name; `IN_IGNORED` on `cgroup.events` (a filesystem that
    removes the file) ends it too. Either publishes a last value holding no
    process, then closes the channel; ctx's end closes it without one, so a
    reader whose ctx is not done reads the close as the group's removal (a
    third bool would have made the value a flag set — KTN-STRUCT-FLAGS).
  - `GroupSignaler` — `Signal(sig)` over the subtree's `cgroup.procs`,
    re-read until a pass finds nobody new (64 passes at most: a tree forking
    faster than it is signalled cannot hold the caller), ESRCH no failure;
    `Contains(pid)`. With `Group.Kill` the Linux group satisfies
    `proc/service`'s `CgroupKill` as it is, and with `GroupReader`,
    `GroupWatcher`, `GroupTree`'s `Path` and `Delegate`, `GroupTuner`'s
    `SetMemoryOOMGroup` and `DeleteBeneath` its `CgroupMembers` and
    `CgroupUnit`: the engine's default adapter over this package is that type
    assertion, which `s1_linux_internal_test.go` pins at compile time.
  - `GroupReader.Members` lists the subtree's `cgroup.procs` (S0g, #734), as
    `Contains`, `Signal` and `populated` reach it: a process a delegated
    service moved into a group of its own is still the group's.
- **Membership(dir)** (S0g, #734) — the path `/proc/<pid>/cgroup` names for a
  process of the group at `dir`, what `AttachValue.Cgroup` takes: `dir` below
  the cgroup2 mount that holds it (the deepest), under the part of the
  hierarchy that mount exposes (`internal/cgroupfs`'s `Mounts`).
  `GroupTree.Path` is a directory, never that path; passed to an
  `AttachValue` as is, it refuses every adoption. `INVALID_SPEC` when no
  cgroup2 mount holds `dir`, `UNSUPPORTED_PLATFORM` off Linux
  (`membership_other.go`).

## Platform notes

- cgroup v2 (unified hierarchy) is **Linux-only**, but two platforms have a
  native equivalent with its own backend (ADR 0018 native-backend roadmap):
  - **Windows** — Job Objects (`cgroup_windows.go`): memory/CPU/pids caps via
    `SetInformationJobObject`; IO and Freeze/Thaw have no stable Job Object
    analogue and return `UnsupportedPlatform`.
  - **FreeBSD** — rctl (`cgroup_freebsd.go`): a group is a tracked PID set and
    limits are per-process `process:PID:vmemoryuse|pcpu:deny=…` rules applied via
    `rctl_add_rule(2)` (re-applied on `Add`). `SetPidsMax` (rctl `maxproc` is a
    user/loginclass/jail resource, not per-process), `SetIOMax` (cgroup-format
    spec), and Freeze/Thaw have no rctl mapping → `UnsupportedPlatform`. Built
    dep-light via raw `syscall.Syscall6` (no `golang.org/x/sys`), the same
    discipline the reaper subreaper backends use. Cross-compile-verified; the
    rule-applying verbs are exercised on the e2e VM lane (real RACCT kernel).
- Off Linux/Windows/FreeBSD the stub reports `Available() == false` and `Create`
  returns `UnsupportedPlatform` (darwin, OpenBSD, NetBSD, DragonFly, illumos,
  Solaris). Every GOOS
  compiles.
- Unprivileged hosts without cgroup delegation degrade gracefully to
  `CgroupUnavailable` — never a panic. The acceptance test
  (`TestGroupConfinement`) gates the live confinement path behind `liveHost`
  (#1097: the root `delegatedRoot` finds — `KITSUNIUM_CGROUP_TEST_ROOT`, the
  mount when `Available()`, this process's own group when `Probe` says it is
  delegated) and, where no root is delegated at all, asserts the typed refusal
  (no handle, `CgroupUnavailable`) instead of skipping. A root whose
  `subtree_control` does not list the case's controller — a delegated scope
  that holds the test — is asserted too: the write is refused typed
  (`CgroupWriteFailed`) and the group stays usable. `Create` without
  `WithRoot` stays `Available()`'s twin
  (`TestCreateWithoutARootFollowsAvailable`).

## Do NOT

- Call `errs.Define` — every code lives in `core/proc`. Restate the sentinel
  fields in `errs.Wrap` instead.
- Support cgroup v1 — only the unified v2 hierarchy is in scope (ADR 0016).
- Assume the top-level mount is writable; always probe (and prefer `WithRoot` to
  a delegated sub-tree).

## Verification

```sh
bazel test --config=race //internal/service/proc/cgroup:cgroup_test
# Fallback
go test -race ./internal/service/proc/cgroup/...
```
