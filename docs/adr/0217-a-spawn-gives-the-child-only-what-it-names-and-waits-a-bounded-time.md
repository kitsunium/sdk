# ADR 0217 — a spawn gives the child only what it names, and waits a bounded time

- **Status**: Accepted
- **Date**: 2026-11-03
- **Amended**: 2026-11-08 — §Amendment: the sandbox (#728, S2); §Amendment: a sticky tmpfs (#728, S2 wired into `proc/service`)
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0016](0016-sdk-process-supervision-domain.md) (three codes, `0.2.6.24`–`0.2.6.26`, and three sibling ports of `Process`), [ADR 0018](0018-sdk-cross-platform-portability.md) (the platform rows of the new `proc/exec` primitives)
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (sibling ports), [ADR 0093](0093-a-sweep-takes-the-zombie-never-the-status.md) (who collects a status), [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md) (measured contracts)

## Context

The 2026-11-02 audit (#802, pass 2 and pass 9) found `proc/exec`'s spawn
generous in two ways and blind in a third:

- **The child inherited more than the `Spec` named.** Go's fork/exec installs
  stdio and `ExtraFiles` with `dup2` and, by the stdlib's stated convention,
  leaves every other descriptor of the supervisor without `FD_CLOEXEC` open in
  the child (#1089): an activation socket, a descriptor the shell leaked, a raw
  `syscall.Dup` — and a child that dropped privileges through `Spec.User` held a
  root-opened one. The runtime also hands every child the open-file soft limit
  it had *before* raising its own (#1091).
- **`Wait` could wait for ever.** Its join of the capture copiers assumed the
  leader was the only holder of the pipes' write ends; a daemonising child, or
  `sh -c 'sleep 60 & exit 0'`, held `Wait`, every later `Wait`, a copier and the
  read end for the grandchild's life (#1088). Nor could a caller bound a wait,
  nor bound the output it captured (#935).
- **A child was reachable only by its number.** `Signal` after a reap could
  reach whatever process held the pid by then; nothing exposed the pidfd or
  HANDLE the runtime already keeps (#928). A supervisor's death left its
  children running (#931); a Windows `Stop` never let a child exit gracefully
  (#933); a child could not get a terminal (#934) or a scheduling class (#940).

## Decision

1. **The descriptor table is sealed by default.** Before every fork, `Start`
   marks every descriptor of the supervisor from 3 up close-on-exec — one
   `close_range(CLOSE_RANGE_CLOEXEC)` on Linux ≥ 5.11 and FreeBSD, the walk of
   `/proc/self/fd` or `/dev/fd` elsewhere. It sets a flag and closes nothing.
   `Spec.FDs` (zero, `FDsSealed`: sealed; `FDsInherited`) keeps fork/exec's default for the caller who
   relies on implicit inheritance. The mark is process-wide and one-way, which
   is accepted: a descriptor the supervisor means a child to have is passed in
   `ExtraFiles`, which the seal never touches. `rlimit.KeepRaisedNoFile` lets
   direct spawns inherit the raised open-file limit without the trampoline.
2. **`Wait` waits a bounded time for output.** `Spec.DrainGrace` bounds the
   copier join after the exit; its zero value is one second — never an
   unbounded wait that a zero would make inert (ADR 0031) — and a negative
   value keeps the unbounded join. Past it the parent's pipe ends are closed and
   `Wait` returns the real `ExitValue` with `STDIO_DRAIN_TIMEOUT` (`0.2.6.24`).
   `ContextWaiter.WaitContext` (ADR 0039) bounds the wait itself, and `exec.Run`
   keeps the head of each stream under a ceiling it refuses at zero.
3. **A process is reachable through a kernel handle.** `ProcessHandle`
   (ADR 0039) exposes a duplicate of the runtime's pidfd or HANDLE and
   `SignalHandle`, which refuses a reaped child with ESRCH instead of signalling
   a number; `HANDLE_UNAVAILABLE` (`0.2.6.25`) names a kernel without pidfd.
   `exec.Open(pid)` holds a process this one did not spawn the same way and
   promises no exit status: its `Wait` is `WAIT_FAILED` wrapping ECHILD. Pid-file
   adoption (#729) builds on it.
4. **The child's lifetime and environment are the Spec's to state.**
   `DieWithParent` (pdeathsig on Linux and FreeBSD, `KILL_ON_JOB_CLOSE` held
   for the supervisor's life on Windows), `Scheduling` (`SCHED_*` and I/O
   class on Linux, `rtprio` on FreeBSD, beside `Nice` on the live pid; `Nice`
   a priority class at creation on Windows), `StdioPTY` with the `Terminal`
   sibling port (Linux, darwin, FreeBSD; `PTY_FAILED`, `0.2.6.26`), and on
   Windows the console control event for a `Setpgid` child's group. Each is
   refused with `UNSUPPORTED_PLATFORM` before spawning where it has no
   mechanism, never dropped (ADR 0018 (a)).
5. **A cgroup placement alone is made by the clone.** On Linux ≥ 5.7 the child
   is cloned into `CgroupPath` (`CLONE_INTO_CGROUP`) and the 5.8× trampoline is
   kept for rlimits, umask and older or filtered kernels.

### ADR 0016 — codes added

| Code | Reason | Emitted by |
|---|---|---|
| `0.2.6.23` | `STDIO_CAPTURE_FAILED` | process (since ADR 0016's table; recorded here) |
| `0.2.6.24` | `STDIO_DRAIN_TIMEOUT` | process |
| `0.2.6.25` | `HANDLE_UNAVAILABLE` | process |
| `0.2.6.26` | `PTY_FAILED` | process |

### ADR 0018 — rows added to the `proc/exec` verdict

| Primitive | Linux | Darwin | FreeBSD | OpenBSD | NetBSD | DragonFly | illumos / Solaris | Windows |
|---|---|---|---|---|---|---|---|---|
| fd-table seal | `close_range` (≥ 5.11), else `/proc/self/fd` | `/dev/fd` | `close_range`, else `/dev/fd` | `/dev/fd` (to 63) | `/dev/fd` | `/dev/fd` | `/proc/self/fd` | nothing to seal |
| `ExitValue.Usage` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ nil | ❌ nil |
| `DieWithParent` | ✅ pdeathsig | ❌ | ✅ pdeathsig | ❌ | ❌ | ❌ | ❌ | ✅ Job Object |
| `Scheduling` | ✅ | ❌ | ⚠️ FIFO/RR/Idle | ❌ | ❌ | ❌ | ❌ | ❌ (`Nice` ✅) |
| `StdioPTY` | ✅ | ✅ | ✅ | ❌ pinsyscalls | ❌ unmeasured | ❌ unmeasured | ❌ STREAMS | ❌ ConPTY |
| `ProcessHandle` / `Open` | ✅ pidfd ≥ 5.4 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ HANDLE |
| placement by clone | ✅ ≥ 5.7 | — | — | — | — | — | — | — |

## Consequences

- **Visible change.** A child no longer inherits a descriptor the supervisor did
  not name; a program that relied on it sets `FDs: FDsInherited`. A `Wait` on a capture
  child whose grandchild holds stdout now returns after a second with
  `STDIO_DRAIN_TIMEOUT` instead of never; the capture is then incomplete by
  design, which the error says.
- **Positive.** No path in the family signals a reaped child's pid; a Windows
  child gets a graceful window; a cgroup-only spawn costs a direct spawn.
- **Accepted.** A `DieWithParent` child on Windows costs one job handle for the
  supervisor's life; Linux's pdeathsig follows the forking thread, which a
  goroutine locked to its thread can end early (documented on the field).
- **Not verified on the authoring host.** The Windows rows (console events,
  priority classes, `DieWithParent`, `Open`), the BSD and darwin rows, and the
  placement by clone (no delegated cgroup) run on e2e-cross and the linux-init
  lane only.

## Alternatives considered

- **Seal in the child, per spawn, so `FDsInherited` is exact.** Go offers no hook
  between fork and exec; doing it in the trampoline would put every spawn
  through the 5.8× re-exec. Rejected for the process-wide flag.
- **A zero `DrainGrace` meaning "unbounded".** It is the behaviour that hung
  `Wait`; a zero that waits for ever is the inert policy ADR 0031 refuses.
- **Widening `Process` with `WaitContext`, `Handle`, `TTY`.** Breaks every
  implementer of a published port (ADR 0039). Siblings instead.
- **Scheduling in the trampoline.** The credential change has happened there,
  so a privileged supervisor could no longer grant a realtime policy to an
  unprivileged child. Applied post-start, beside `Nice`, with its window stated.

## Amendment 2026-11-08 — the sandbox (#728, S2)

### Context

The systemd-parity series (supervizio/agent#326, consumer #331) needs a child
spawned confined: namespaces, mounts, a new root, a capability bounding set,
an identity, no_new_privs, Landlock and seccomp. Go's `os.StartProcess` sets
namespaces and an identity between fork and exec, nothing else, and every
other attribute is per THREAD (`setuid`, `prctl`, capabilities, Landlock,
seccomp): applied on a thread other than the one that calls `execve`, the
program would run without it.

### Decision

1. **`Spec.Sandbox` (`SandboxValue`), applied by a trampoline in two stages.**
   Stage one is the clone: the namespaces, the mount namespace unshared and
   made private (`MS_REC|MS_PRIVATE`, so nothing the child mounts propagates
   back), a user namespace's maps with `setgroups` denied, and the exec of
   `/proc/self/exe` — the running inode, never a path that may since hold
   another binary. No `os.Executable` fallback: without procfs the spawn is
   refused. Stage two runs in the child before `main`, on the main thread the
   runtime keeps locked during package initialisation (and locks again), in
   the order the kernel imposes: the cgroup namespace, mounts, `pivot_root`,
   the Landlock paths opened `O_PATH`, `PR_CAPBSET_DROP`, securebits, `setgroups`/`setresgid`/
   `setresuid` as raw calls on that thread (the parent-death signal armed
   again after, as the kernel clears it), `capset` and the ambient set, the
   close-on-exec sweep past the handshake pipe, `no_new_privs`,
   `landlock_restrict_self`, and the seccomp filter in the system call before
   `execve`. `Spec.Credential`'s clone-time identity is not used: the stage
   takes it after the bounding set is cut. Nor is the cgroup namespace taken
   at the clone: a `Spec.CgroupPath` the clone does not place (a kernel before
   5.7, clone3 filtered) is placed by the trampoline's write, and a namespace
   taken before it is rooted at the supervisor's group — the child sees its
   group beneath that root, and a group outside it refuses the write. The
   stage unshares it first, the child in its group on either path, so the
   namespace is rooted at that group.
2. **Fail closed.** The parent refuses before the fork what cannot be
   applied — `UNSUPPORTED_PLATFORM` off Linux amd64/arm64,
   `SANDBOX_UNAVAILABLE` (`0.2.6.36`) for a part the kernel lacks, naming it
   in `feature` (`landlock`, `landlock:<right>` with `abi`, `seccomp`,
   `namespace:<kind>`, `procfs`, `memfd_create`, `mount_setattr`,
   `cap_last_cap`, `capability`), `INVALID_SPEC` for a malformed value. A step
   that fails in the child writes its number and errno on the handshake pipe
   and exits: `SANDBOX_FAILED` (`0.2.6.30`) names the step, and the program
   never runs.
3. **The payload is sealed.** The stage reads its instructions from a memfd
   the parent wrote and sealed (`F_SEAL_WRITE|GROW|SHRINK|SEAL`): a magic, a
   version, a length that must be the file's size, every count and string
   bounded, no byte left over. Anything else is refused. The descriptor's
   number travels in `__KITSUNIUM_SDK_PROC_SANDBOX`, listed in
   `reservedEnvKeys` (ADR 0219 §1).
4. **Seccomp is classic BPF written by hand** — no module, the kernel and core
   stay stdlib-only (cilium/ebpf is the vendor module of ADR 0215 and is not
   imported). The filter checks the architecture first (another ABI, and
   amd64's x32, is killed), lets through the floor the stage needs
   (`execve`, `exit`, `exit_group`, `rt_sigreturn`, and `write` on the
   handshake descriptor alone), then the named calls and the default. The
   system call numbers and the name table are written per GOARCH — amd64 and
   arm64 — and pinned by a test on each; every other GOARCH refuses the field.
5. **Landlock returns its ABI.** `exec.LandlockABI` (and `process.LandlockABI`)
   returns the kernel's version; a right newer than it is refused unless the
   `LandlockValue` names it best-effort, in which case it is left
   unrestricted. `CapSandbox` (13) is the capability row.
6. **Spikes, not shipped.** OpenBSD `pledge`/`unveil` and Windows restricted
   tokens/AppContainer were weighed: neither maps onto the Linux steps
   (`pledge` restricts by promise, `unveil` does not survive `execve`; a token
   is set at `CreateProcessAsUser` with no namespace, mount, Landlock or
   seccomp equivalent). Both stay `UNSUPPORTED_PLATFORM`; the verdict is in
   `design/proc.yaml`.

### ADR 0016 — codes added

| Code | Reason | Emitted by |
|---|---|---|
| `0.2.6.30` | `SANDBOX_FAILED` | process |
| `0.2.6.36` | `SANDBOX_UNAVAILABLE` | process |

### ADR 0018 — row added to the `proc/exec` verdict

| Primitive | Linux | Darwin | FreeBSD | OpenBSD | NetBSD | DragonFly | illumos / Solaris | Windows |
|---|---|---|---|---|---|---|---|---|
| `Spec.Sandbox` | ✅ amd64, arm64 (❌ other GOARCH) | ❌ | ❌ | ❌ spike | ❌ | ❌ | ❌ | ❌ spike |

### Consequences

- **Positive.** A sandbox asked for is applied whole or the program does not
  run; the order and the thread are tested on the child itself.
- **Accepted.** An unprivileged supervisor reaches namespaces only through its
  own user namespace, whose single map line makes the child root inside it and
  forbids `setgroups`: `Spec.Groups` beside `NamespaceUser` is `INVALID_SPEC`.
  A `DieWithParent` child in its own PID namespace cannot compare its parent's
  pid after its identity changes; the signal is armed again, unchecked.
- **Not verified on the authoring host.** arm64 (table pinned, filter never
  loaded), kernels without Landlock or `mount_setattr`, an identity change to
  another user (needs a privileged supervisor), and the darwin, BSD, illumos,
  Solaris and Windows refusals (cross-compiled; they run on e2e-cross).

## Amendment 2026-11-08 — a sticky tmpfs (#728, S2 wired into `proc/service`)

`proc/service` turns `PrivateTmp=disconnected` into a tmpfs on `/tmp` and on
`/var/tmp` (ADR 0214's amendment of the same day). S2's `MountTmpfs` is mode
0755, owned by the identity the stage has when it mounts it — before it takes
the spec's —, so a `User=` other than that one could not write to its own
`/tmp`. `MountTmpfsSticky` (`3`) mounts it as systemd does: mode 1777,
`nosuid`, `nodev`, read-only when asked. The payload carries a mount's kind as
a byte already: its version does not change, and a kind past
`MountTmpfsSticky` is `INVALID_SPEC` before the fork, as before.
`TestSandboxMounts` reads both modes in the child.

## Amendment 2026-11-08 — a name `Spec.Env` repeats reaches the child once, with its last value (#738)

`Spec.Env` went into the child's block as written, by `buildProcAttr` on Unix
and `spawnEnv` on Windows, and a child's `getenv` — the Go runtime's first of
all — reads the first entry of a name. But `append(os.Environ(), "K=v")` is
`os/exec`'s documented way to replace a variable, the last entry winning, and
the SDK already read the last: `searchPathOf` searched `Spec.Env`'s last
`PATH`, `EnvAllow` and `EnvDeny` kept a repeated key's last value. So the SDK
searched one value and ran with another: a `PATH` searched was not the `PATH`
the child read, and a supervisor that meant to replace an inherited
`LD_PRELOAD`, `PATH` or `NOTIFY_SOCKET` replaced nothing. The same day it made
a fork bomb of a test: a supervisor's child read the supervisor's role out of
`append(os.Environ(), helper+"=child")` through `proc/service`'s `mergeEnv`,
which kept the first of a name, and supervised in turn.

1. **Each variable once, its last value, at the place of its last
   occurrence.** `dedupEnv`, in `buildProcAttr` and `spawnEnv`, one map over the
   entries, names compared as the platform compares them (`variableKey`: on
   Windows the fold classes `strings.EqualFold` compares by, which a plain
   `strings.ToLower` would split). A nil `Env` is still an empty environment, an
   entry without `=` is kept as written, the trampoline's sentinel still comes
   after.
2. **`envKey` reads a leading `=` as part of the name**, as `os/exec` does:
   `=C:=C:\dir` and `=D:=D:\dir` are two variables and used to overwrite each
   other in `EnvAllow` and `EnvDeny`.
3. **`mergeEnv` follows the same rule.** A repeated name of the template keeps
   its last value at its last place, a name the engine sets replaces every
   entry, whatever the case on Windows, where the template's `Path=` is the
   unit's `PATH`.
4. **Consequences.** A caller that read the first of two entries — `mergeEnv`
   was the one in the tree — now reads the last, as every program that spawns
   through `os/exec` does. `TestStartEachVariableOnce` and
   `TestStartSearchesThePathTheChildReads` spawn `/usr/bin/env` on both paths;
   on the base the direct path handed every occurrence on, the trampoline the
   first one's value, and `PATH` was the one not searched.

## References

- Issues #928, #929, #931, #933, #934, #935, #940, #943, #1088, #1089, #1090, #1091; #728, #738 (amendments)
- `internal/service/proc/exec/CLAUDE.md`, `BENCH.md`; `internal/service/proc/childwait/BENCH.md`
