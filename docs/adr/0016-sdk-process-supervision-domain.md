# ADR 0016 — OS process-supervision domain (`proc`)

**Status**: Accepted
**Date**: 2026-06-14
**Deciders**: @kodflow
**Amended by**: [ADR 0155](0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md) — the facades move under `pkg/v1/proc`, `sdnotify` and `sdlisten` as `systemd/notify` and `systemd/listen`; [ADR 0214](0214-a-service-unit-is-one-actor-over-a-pure-reducer.md) (2026-11-03) — §Context: the life cycle of ONE service moves into the SDK as `proc/service`; [ADR 0217](0217-a-spawn-gives-the-child-only-what-it-names-and-waits-a-bounded-time.md) (2026-11-03) — codes `0.2.6.24`–`0.2.6.26` and the `ContextWaiter`, `ProcessHandle` and `Terminal` sibling ports; [ADR 0219](0219-a-supervisor-adopts-hears-and-hands-over-only-what-it-can-verify.md) (2026-11-07) — codes `0.2.6.31`–`0.2.6.35`, `AttachValue`, `DescriptorsValue` and `Spec.PIDEnv`; this ADR, §Amendment 2026-11-08 (kitsunium/sdk#727, S1) — nested control groups, controllers, typed cgroup v2 setters, the `cgroup.events` watch, `CGROUP_CONTROLLER_ABSENT` (`0.2.6.37`); [ADR 0215](0215-a-cgroups-bpf-programs-are-built-in-go-in-a-quarantined-vendor-module.md) (2026-11-08, kitsunium/sdk#731, S6) — the cgroup BPF programs: a contract `core/proc/bpf` (`0.3.107.*`), an engine `service/proc/bpf` and a facade `pkg/v1/proc/bpf`, whose backend is the vendor module `third-party/ebpf`; [ADR 0217](0217-a-spawn-gives-the-child-only-what-it-names-and-waits-a-bounded-time.md) §Amendment 2026-11-08 (#728, S2) — `Spec.Sandbox`, `SANDBOX_FAILED` (`0.2.6.30`), `SANDBOX_UNAVAILABLE` (`0.2.6.36`)
**Related**: ADR 0001 (multi-module layout), ADR 0005 (dotted-quad error codes),
ADR 0013 (crypto domain — sibling-admission precedent), ADR 0014 (the verb wave)

## Context

`supervizio/agent` (superviz.io) — a PID1-capable process supervisor — is being
refactored to depend on this SDK and to reach **systemd-minimum feature parity**.
The reusable, business-agnostic OS plumbing it needs (spawn a process with a
precise attribute set, signal it, reap it, confine it, learn when it is *ready*)
does not exist in the SDK today. The supervisor's *policy* (restart/backoff,
dependency topological ordering, health probes, YAML config) stays in the
product; only the **OS primitives** belong here.

These primitives are stdlib-plus OS plumbing with **zero business logic**. They
fit the SDK's `kernel → core → service → pkg/v1` layering exactly as codec and
crypto do, and they extend the existing precedent of the SDK owning low-level
primitives (kernel `ring`/`clock`, the codec and crypto domains).

This is the **sixth** `internal/core` sibling beside `codec`, `writer`, `crypto`,
`logger`, and `transform`. Per `internal/core/CLAUDE.md`, admitting a new sibling
requires widening the layer's purpose statement — this ADR does so.

## Decision

Admit a new core domain `internal/core/proc`: the **OS process-supervision**
contract. It is a *foundation* package — interfaces, immutable value types, and
the domain's complete error-sentinel set — consumed by service implementations
and re-exported by six `pkg/v1` facades.

### Layering

```
internal/core/proc/                 contract: interfaces + value types + sentinels (0.2.6.*)
internal/service/proc/<impl>/       linux/unix implementations + portable stubs
pkg/v1/{process,signal,reaper,      thin facades (aliases + ergonomic helpers)
        rlimit,cgroup,sdnotify}
```

Unlike codec/crypto, `proc` ships **no plug-in registry**: each primitive has a
single canonical OS implementation selected at build time by platform tag, not a
runtime-registered scheme. Core declares the ports; service satisfies them; the
facades re-export. The `Signal` value type and its name↔number table live in
`core/proc` (build-tagged) so `Stop(sig)` / `Relay` / `Parse` all share one
source of truth.

### Surface (core ports + values)

| File | Surface |
|---|---|
| `proc.go` | package doc + `Resource` enum (NOFILE, NPROC, CORE, AS, CPU, FSIZE, …) |
| `signal.go` (+ `signal_unix.go` / `signal_other.go`) | `Signal int` value type: `Parse` / `String` / `OS`; platform name↔number table |
| `spec.go` | `Spec` — process spawn spec (path/args/dir/env, creds, pgroup/session, rlimit/nice/umask/oom) |
| `exit.go` | `ExitValue` — exit code, terminating signal, CPU times, max RSS |
| `limit.go` | `LimitValue` — soft/hard rlimit pair |
| `notification.go` | `NotificationValue` — a parsed sd_notify datagram + validated sender PID |
| `process.go` | `Process` interface — `PID` / `Wait` / `Signal` / `SignalGroup` / `Stop` |
| `reaper.go` | `Reaper` interface — `Start` / `Stop` / `ReapOnce` |
| `group.go` | `Group` interface — cgroup v2 `SetMemoryMax` / `SetCPUMax` / `SetPidsMax` / `SetIOMax` / `Add` / `Delete` |
| `listener.go` | `Listener` interface — sd_notify supervisor side `Recv` / `Close` |
| `codes.go` / `errors.go` | dotted-quad codes + sentinels, range `0.2.6.*` |

### Error-code allocation (range `0.2.6.*`, Major 0 / Layer 2 / Package 6)

The **whole domain** owns one `PP` octet and declares every sentinel centrally in
`internal/core/proc`. Service implementations and facades only `errs.Wrap` these
sentinels — they declare **zero** new codes. This keeps the per-package code
allocation conflict-free across the six facades and keeps the AST audit's view
(only `core/proc` calls `errs.Define`) trivially unique.

| Code | Reason | Emitted by |
|---|---|---|
| `0.2.6.1` | `UNSUPPORTED_PLATFORM` | any primitive on a non-supporting OS |
| `0.2.6.2` | `INVALID_SPEC` | process |
| `0.2.6.3` | `UNKNOWN_USER` | process |
| `0.2.6.4` | `UNKNOWN_GROUP` | process |
| `0.2.6.5` | `SPAWN_FAILED` | process |
| `0.2.6.6` | `WAIT_FAILED` | process |
| `0.2.6.7` | `SIGNAL_FAILED` | process / signal |
| `0.2.6.8` | `STOP_FAILED` | process |
| `0.2.6.9` | `UNKNOWN_SIGNAL` | signal |
| `0.2.6.10` | `RELAY_FAILED` | signal |
| `0.2.6.11` | `SUBREAPER_FAILED` | reaper |
| `0.2.6.12` | `REAP_FAILED` | reaper |
| `0.2.6.13` | `UNKNOWN_RESOURCE` | rlimit |
| `0.2.6.14` | `RLIMIT_FAILED` | rlimit |
| `0.2.6.15` | `CGROUP_UNAVAILABLE` | cgroup |
| `0.2.6.16` | `CGROUP_CREATE_FAILED` | cgroup |
| `0.2.6.17` | `CGROUP_WRITE_FAILED` | cgroup |
| `0.2.6.18` | `CGROUP_DELETE_FAILED` | cgroup |
| `0.2.6.19` | `NOTIFY_FAILED` | sdnotify |
| `0.2.6.20` | `LISTEN_FAILED` | sdnotify |
| `0.2.6.21` | `INVALID_NOTIFICATION` | sdnotify |
| `0.2.6.22` | `CREDENTIAL_MISMATCH` | sdnotify |

### Platform discipline

- `process`, `signal`, `rlimit` — Unix (linux/darwin/bsd) via `SysProcAttr` +
  post-fork `setrlimit`/`setpriority`/`umask`. Windows: documented best-effort /
  no-op with `UNSUPPORTED_PLATFORM`.
- `reaper` (subreaper), `cgroup`, `sdnotify` listener credentials — Linux-only.
  Non-supporting builds return a no-op + `UNSUPPORTED_PLATFORM`, never panic.
- Every package builds on every platform (portable stub files); only behaviour
  degrades, never compilation.

### Dependency discipline

`core/proc` is **stdlib-only** (`os`, `syscall`, `time`, `strconv`, `strings`)
plus `internal/kernel/errs`. Service implementations stay stdlib-only too
(`syscall`, `os`, `os/signal`, `net` for the AF_UNIX datagram) — no
`golang.org/x/sys`, preserving the SDK's dep-light invariant. The stdlib
`syscall` package covers everything needed: `Setrlimit`, `Setpriority`,
`Credential`, `Wait4`, `Kill`, `ParseUnixCredentials`, and `Syscall(SYS_PRCTL,…)`.

## Consequences

- **Positive.** superviz.io's process layer reduces to consuming six SDK
  facades; the OS plumbing is tested once, here, with race detection. Any
  container-entrypoint or init-shaped Go program reuses the same primitives.
- **Positive.** Central error allocation means the six facades are developed and
  reviewed independently with no shared-file contention.
- **Negative / accepted.** Six new public packages enlarge the v1 surface and the
  freeze obligation. They are scoped to OS primitives only; policy stays in the
  consumer, bounding growth.
- **Negative / accepted.** Some acceptance tests (cgroup confinement, subreaper
  reparenting) require Linux + privilege/delegation and are gated at runtime
  (`Available()` / capability checks) so they skip cleanly elsewhere.

## Enforcement

- `internal/core/CLAUDE.md` purpose statement + sibling table widened to admit
  `proc/` (this ADR is the authorising record).
- Codes are audited by `//internal/kernel/errs:errs_test` (AST audit) — uniqueness
  + `reason == screamingSnake(varName)`, identical to every other domain.
- A `pkg/v1/<facade>` tag (ADR 0007 sub-directory convention) publishes the
  domain to downstreams; see issue #66.

## Amendment — 2026-11-08: S1, the cgroup v2 mechanisms a supervisor writes through (kitsunium/sdk#727)

**Context.** The agent (supervizio/agent#330) wrote `cgroup.subtree_control`
itself and made a flat `svc-<pid>` group, because `cgroup.Create` refused a
nested name and the SDK had no setter past `memory.max`, `cpu.max`,
`pids.max` and `io.max`. S0g (#734) needs a group per unit it can kill,
watch until empty and hand over.

**Decision.** The SDK becomes the only writer of a group's interface files:

- `Create` takes a nested name on Linux. Every element is literal; the
  missing groups are made top-down through an `os.Root` opened on the root,
  so a `..` or a link cannot take a write outside it; an existing element
  that is not a directory is `INVALID_SPEC`; what a failed call made is
  removed. `WithControllers` enables controllers in the
  `cgroup.subtree_control` of the root and of every group on the way.
- Five sibling ports of `Group` (ADR 0039), never methods added to it:
  `GroupTuner` (`memory.min/low/high/swap.max/zswap.max/zswap.writeback/oom.group`,
  `cpu.weight`, `cpu.max.burst`, `cpuset.cpus/mems`, `io.weight`,
  `io.latency`, typed values, out-of-range ones `INVALID_SPEC`), `GroupTree`
  (`Path`, `EnableControllers`, `Delegate`, `DeleteTree` — a delegated
  unit's subtree removed at its stop), `GroupWatcher` (`Watch`: the
  state, then each change, coalesced, never blocking), `GroupSignaler`
  (`Signal`, `Contains` — with `Kill`, `proc/service`'s `CgroupKill`) on
  Linux, and `JobLimiter` (`SetJobMemoryLimit`) on Windows, where
  `SetMemoryMax` stays per-process. A platform without the mechanism has no
  such port: the failed assertion is its refusal.
- One code: `0.2.6.37` `CGROUP_CONTROLLER_ABSENT`, a controller the
  parent does not offer. `0.2.6.38` is held in reserve.
- The Job Object freeze and I/O rate stay refused (`design/proc.yaml`);
  no systemd slice, no OOM policy: values only.

**As built.** On cgroupfs an `rmdir` of a group raises no `IN_IGNORED` for a
watch on its files — kernfs removes them without the VFS — so `Watch` also
watches the parent directory for the group's `IN_DELETE` (measured on Linux
6.12 in a `Delegate=yes` scope). Delegation chowns the files
`/sys/kernel/cgroup/delegate` lists, the three of cgroup-v2.rst without it.
The live suite runs in a delegated scope; io, cpuset and hugetlb are not
delegated to a user manager, so their setters ran on the fake filesystem
only.
