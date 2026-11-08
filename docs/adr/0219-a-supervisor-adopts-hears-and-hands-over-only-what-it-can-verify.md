# ADR 0219 — a supervisor adopts, hears and hands over only what it can verify

- **Status**: Accepted
- **Date**: 2026-11-07
- **Amended**: 2026-11-08 — §Amendment: adoption on darwin and the BSDs (#729); §Amendment: the notify listener on FreeBSD (#730)
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0016](0016-sdk-process-supervision-domain.md) (five codes, `0.2.6.31`–`0.2.6.35`, `AttachValue`, `DescriptorsValue`, `Spec.PIDEnv`), [ADR 0018](0018-sdk-cross-platform-portability.md) (the platform rows of adoption, the notify transport and `PIDEnv`)
- **Related**: [ADR 0214](0214-a-service-unit-is-one-actor-over-a-pure-reducer.md) (the service engine these feed: S0c, S0d, S0f, S0h), [ADR 0217](0217-a-spawn-gives-the-child-only-what-it-names-and-waits-a-bounded-time.md) (`ProcessHandle`, `exec.Open`, the sealed descriptor table), [ADR 0093](0093-a-sweep-takes-the-zombie-never-the-status.md) (who collects a status), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md)

## Context

The systemd-parity series (supervizio/agent#326) puts a service manager's
foundations in `proc`. Four of them were missing or unsafe:

- **The spawn protocol could be hijacked (#726).** The trampoline tells its
  child what to do through `__KITSUNIUM_SDK_PROC_*` variables appended AFTER
  `Spec.Env`, and a child's `getenv` reads the FIRST of two equal keys: a
  caller's `__KITSUNIUM_SDK_PROC_CGROUP` made the trampoline write its pid into
  any path the caller named.
- **No process could be adopted (#729).** A supervisor supervised its own
  children only; nothing read a PID file without the races systemd guards
  against — a symbolic link, a file another user rewrites, a PID reused.
- **The notify socket was half read (#730).** `Recv` returned the text and the
  sender's PID. A descriptor a datagram carried (`SCM_RIGHTS`) landed in the
  supervisor's table and was never returned nor closed — three leaked per
  datagram —, and no assignment was bounded (`READY=2`, `MAINPID=0`).
- **A child could not learn that the sockets were its own (#739).**
  `LISTEN_PID` was omitted, since no parent knows a child's PID before forking
  it, and any name was accepted into `LISTEN_FDNAMES` (`a:b` announced two
  sockets for one).

## Decision

1. **The spawn protocol's variables are the SDK's.** `Start` and `Run` refuse,
   before any fork and on every platform, a `Spec.Env` key that begins
   `__KITSUNIUM_SDK_PROC_` in any case: `RESERVED_ENV` (`0.2.6.31`). The
   variables are listed once (`reservedEnvKeys`), which the trampoline strips;
   a test parses the package's sources and fails when a constant with the
   prefix is neither listed nor given a refusal case.
2. **A process is adopted through a pidfd, its end observed, its status never
   claimed.** `exec.Attach(pid)` holds a non-blocking pidfd in the runtime's
   poller (`PIDFD_NONBLOCK`, or `fcntl` before Linux 5.10); its `Wait` returns
   once the process has ended with `STATUS_UNKNOWN` (`0.2.6.34`) and `Code -1`.
   It never calls `wait4`, so a child attached here keeps its status for its
   spawner or the reaper. `exec.AttachPIDFile(path, want)` reads a PID file
   safely — an absolute clean path walked from the root, each directory held
   `O_PATH`, the last component examined `O_PATH|O_NOFOLLOW` before any read
   (`PID_FILE_REFUSED`, `0.2.6.32`, for a link, a FIFO, a device, garbage) — and
   trusts it when root or this process's user made everything the walk went
   through, as systemd trusts a root-owned PID file. Any other file adopts only
   within `want` (`AttachValue`: a cgroup v2 subtree, a set of users), read
   from `/proc` while the pidfd pins the process and proven to concern it; PID
   1, this process and an unbounded untrustworthy file are `ADOPTION_REFUSED`
   (`0.2.6.33`). `proc/service.NewAttacher(scope)` is the engine's `Attacher`
   over them.
3. **The notify listener reads the whole transport and keeps nothing it was
   not asked to.** The control buffer holds `SCM_MAX_FD` descriptors, the
   credentials and `SO_PASSPIDFD`'s pidfd (Linux 6.5); descriptors arrive
   close-on-exec (`MSG_CMSG_CLOEXEC`). A truncated datagram, truncated control
   data, an embedded NUL, an empty name or an assignment sd_notify(3) defines
   out of its bounds is refused with every descriptor closed. `BARRIER=1` is
   returned alone with its one descriptor, `MAINPIDFD=1` as a pidfd with the
   PID the kernel gives, `FDSTORE=1`'s as files; any other is closed, as
   systemd's manager does. The sender's uid and gid are exposed; judging them
   is the caller's. `ListenFor(uid, gid)` hands the socket to the service's
   user.
4. **A child is told its own PID after the fork.** `Spec.PIDEnv` names
   variables the trampoline sets to its PID — the target's, `execve` keeping
   it — replacing `Env`'s. `systemd/listen`'s `Prepare` and `PrepareFiles` put
   `LISTEN_PID` there, check every name (`INVALID_FD_NAME`, `0.2.6.35`,
   systemd's `fdname_is_valid`), announce an empty one as `unknown`, and
   `PrepareFiles` hands a store's descriptors as they are — never duplicated
   nor closed.

### ADR 0016 — codes added

| Code | Reason | Emitted by |
|---|---|---|
| `0.2.6.31` | `RESERVED_ENV` | process (`Start`, `Run`) |
| `0.2.6.32` | `PID_FILE_REFUSED` | process (`AttachPIDFile`) |
| `0.2.6.33` | `ADOPTION_REFUSED` | process (`Attach`, `AttachPIDFile`) |
| `0.2.6.34` | `STATUS_UNKNOWN` | process (an attached process's `Wait`) |
| `0.2.6.35` | `INVALID_FD_NAME` | systemd/listen (`CheckName`, `Prepare`, `PrepareFiles`) |

### ADR 0018 — rows added to the `proc` verdict

| Primitive | Linux | Darwin | FreeBSD | OpenBSD / NetBSD / DragonFly | illumos / Solaris | Windows |
|---|---|---|---|---|---|---|
| `ReservedEnv` refusal | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| `Attach` / `AttachPIDFile` | ✅ pidfd ≥ 5.3, poller ≥ 5.3 (`fcntl` before 5.10) | ❌ | ❌ | ❌ | ❌ | ❌ |
| notify `Recv` with descriptors | ✅ (`SCM_PIDFD` ≥ 6.5) | ❌ | ❌ | ❌ | ❌ | ❌ |
| `Spec.PIDEnv` | ✅ trampoline | ✅ trampoline | ✅ trampoline | ✅ trampoline | ✅ trampoline | ❌ |
| `PrepareFiles`, `CheckName` | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ (`CheckName` ✅) |

## Consequences

- **Visible change.** A `Spec.Env` naming `__KITSUNIUM_SDK_PROC_*` is refused.
  A notify datagram with an assignment out of bounds is refused whole —
  stricter than systemd, which drops the one assignment —, and descriptors no
  assignment called for are closed. `Prepare` refuses a name with `:` and puts
  every activated spawn through the trampoline (one re-exec of the
  supervisor's binary) so the child reads its own `LISTEN_PID`; a caller that
  cannot afford it removes `LISTEN_PID` from `PIDEnv`. `NotificationValue`
  gains `SenderUID`, `SenderGID` and `Descriptors`.
- **Positive.** S0c reads no PID file itself; S0d receives descriptors and
  re-emits them with their names; S0f hands a connection as `LISTEN_FDS=1`; the
  engine's notification reader can no longer be ended by a sender of garbage.
- **Not done.** The BSD and darwin half of #729 (kqueue `EVFILT_PROC` and a
  start time) and the FreeBSD half of #730 (`LOCAL_CREDS_PERSISTENT`): no
  runner of this lot could prove them, so both stay `UNSUPPORTED_PLATFORM`.
- **Not verified on the authoring host.** Linux before 5.10 (the `fcntl` path
  is driven by an injected opener), a kernel without `SO_PASSPIDFD`, ownership
  by another user (the tests run as one user), and every non-Linux row beyond
  compilation.

## Alternatives considered

- **Refuse only the three keys in use.** A fourth variable would be open the
  day it is added; the prefix, in any case, closes the class.
- **Adopt through `exec.Open`.** Its `Wait` is refused by design (ADR 0217); an
  adopter needs the end. A second handle shares the pidfd idiom without
  changing `Open`'s contract.
- **Trust a PID file by its owner alone.** Whoever can write the directory can
  replace the file; the walk judges every step, as `chase(CHASE_SAFE)` does.
- **Claim childwait's status for an adopted child.** A pid can be reused once
  reaped, even while a pidfd is open, so a claim by number could capture a
  stranger's status. The status is left to the spawner or the reaper.
- **Keep omitting `LISTEN_PID`.** It is the protocol's guard against a
  descendant taking the sockets; only a post-fork step can set it, and the
  trampoline is that step.

## References

- Issues #726, #729, #730, #739; the series supervizio/agent#326 (S0c #735,
  S0d #736, S0f #740, S0h #738)
- `internal/service/proc/exec/CLAUDE.md`, `internal/service/proc/systemd/notify/CLAUDE.md`,
  `internal/service/proc/systemd/listen/CLAUDE.md`, `internal/service/proc/service/CLAUDE.md`

## Amendment — 2026-11-08: adoption on darwin and the BSDs (#729)

The darwin and BSD half of #729, "not done" above, is done. Where it departs
from the issue's letter, the reason is given; what never ran is said.

1. **The handle.** On darwin, FreeBSD, OpenBSD, NetBSD and DragonFly
   `exec.Attach` holds the process by two kqueue `EVFILT_PROC` knotes
   (`NOTE_EXIT`), each registered on its own close-on-exec kqueue. The kernel
   attaches a knote to the process that has the pid at registration and to no
   later one, which is what the pidfd gives on Linux. The watcher blocks in
   `kevent(2)` on the first — a kqueue is not a descriptor the runtime's
   poller takes, so a handle being waited on holds one thread until the end;
   the second is only ever polled, under a lock, so that no wait consumes the
   event a signal must be refused on. `Wait` returns `STATUS_UNKNOWN` with
   `Code -1` as on Linux, and nothing calls `wait4`. The issue's `EVFILT_USER`
   wake-up is not used: the handle has no `Close`, so the watcher has nothing
   to be woken for but the end.
2. **Signals go by pid, after a proof.** No kernel handle signals a process
   these kernels did not let this one fork (FreeBSD's process descriptors come
   from `pdfork(2)` only). `Signal` and `SignalGroup` poll the second knote and
   call `kill(2)` only while it is silent; once it has fired they answer
   `SIGNAL_FAILED` wrapping ESRCH and send nothing. The instant between the
   poll and `kill(2)` is the one window a PID reuse could still use: the
   process would have to exit, be reaped by its parent and its pid be handed
   out again between two consecutive system calls. `SignalHandle` is
   `UNSUPPORTED_PLATFORM` and `Handle` reports none, as for a spawned process
   on these kernels.
3. **The start time guards the reads, not the signals.** A users bound is read
   from the kernel's process record — `kern.proc.pid` (`struct kinfo_proc`) on
   FreeBSD, DragonFly and darwin, `KERN_PROC` on OpenBSD, `KERN_PROC2`
   (`struct kinfo_proc2`) on NetBSD — once before the knotes are registered
   and once after: the two must state one start time, a pid that changed hands
   in between being refused with ESRCH, and the knote still silent afterwards
   proves the second read concerned the process held. The start time is not
   compared again before a signal, as the issue suggests: FreeBSD and OpenBSD
   compute it on every read as the process's uptime plus the boot time
   (`kern_proc.c`, `fill_kinfo_proc_only`; `kern_sysctl.c`, `fill_kproc`), and
   a stepped clock moves the boot time. A live process would then read as
   another, and `Stop`, which takes ESRCH for "already gone", would return
   success with it running. The knote does not depend on the clock.
4. **The records' layouts** are decoded by `proc/internal/bsdabi`, which
   holds no system call and no build constraint: `ProcRequest` names the
   sysctl and the buffer, `ProcRecord` decodes a record only once its size,
   the size it states (FreeBSD's `ki_structsize`) and the pid it names prove
   it the one asked for — a record it cannot prove is `HANDLE_UNAVAILABLE`,
   never a guess. Every offset is the header's under the architecture's ABI —
   FreeBSD LP64, i386 and arm, DragonFly amd64, darwin amd64 and arm64, and
   OpenBSD and NetBSD, whose fixed-width records are laid out alike on every
   architecture and are asked for the prefix read. Each was computed twice:
   by a C compiler from the header, and from the cgo `-godefs` transcriptions
   two published Go modules carry where they exist (FreeBSD, darwin). The
   tests lay every record out member by member from a transcription of its
   header, so a wrong offset fails on any machine.
5. **The PID file.** The walk is one code on every kernel; only the directory
   holder differs. Off Linux there is no `O_PATH`: each directory is held as
   an `os.Root` — `openat(2)` with `O_NOFOLLOW`, `fstatat(2)`, `readlinkat(2)`,
   which the stdlib reaches on every one of these kernels, OpenBSD's and
   darwin's libc included —, so the walk needs read permission on each
   directory where Linux needs search permission only. The last component is
   examined by `fstatat` before anything opens it, then opened `O_NONBLOCK`
   and proven by device and inode to be the file examined.
6. **No cgroup.** A `Cgroup` bound is `ADOPTION_REFUSED` off Linux: it cannot
   be checked there, and a bound nobody checks is not one.
7. **The row of ADR 0018's verdict**, restated:

   | Primitive | Linux | Darwin | FreeBSD | OpenBSD / NetBSD / DragonFly | illumos / Solaris | Windows |
   |---|---|---|---|---|---|---|
   | `Attach` / `AttachPIDFile` | ✅ pidfd ≥ 5.3, poller ≥ 5.3 (`fcntl` before 5.10) | ✅¹ kqueue | ✅¹ kqueue | ✅¹ kqueue (DragonFly compiled only) | ❌ | ❌ |

   ¹ never run on the authoring host; a signal goes by pid once a knote has
   proven the process alive; a cgroup bound is refused.
8. **Never run.** The authoring host is Linux and no darwin or BSD machine was
   at hand. What ran is the decoding of every layout from header-built
   records (`bsdabi`), the walk over its Linux holder, and the compilation and
   `go vet` of every target, tests included. The tests written for the kernels
   themselves — the record read against the kernel's own answer for this
   process and a child, the knote firing at the exit before the reap, a reused
   pid simulated with a second child of the test and refused, the start time
   and users bounds, `Stop` — run on the e2e-cross lanes: macOS, FreeBSD 15.0,
   OpenBSD 7.9 and NetBSD 10.1. DragonFly is compiled only.

## Amendment — 2026-11-08: the notify listener on FreeBSD (#730)

The FreeBSD half of #730, "not done" above, is done; darwin, OpenBSD, NetBSD
and DragonFly keep the typed refusal the issue gives them.

1. **The credentials.** `Listen` and `ListenFor` set `LOCAL_CREDS_PERSISTENT`
   (`SOL_LOCAL` 0, option 3, sys/un.h, hard-coded — the stdlib names neither)
   on the socket: the kernel then prepends to every datagram an `SCM_CREDS2`
   control message (0x08), a `struct sockcred2` it fills itself and refuses in
   a message sent (`unp_internalize`), so a sender cannot forge it. It is
   decoded by `proc/internal/bsdabi.Sockcred2` — version 0, the groups it
   counts present, at most `CMGROUP_MAX` — into `SenderPID` and the REAL
   `SenderUID` and `SenderGID`, the ids Linux's `SCM_CREDENTIALS` carries by
   default. A datagram without it — the kernel could not allocate it — is
   `CREDENTIAL_MISMATCH`, every descriptor it carried closed, as on Linux.
2. **The descriptors arrive close-on-exec atomically.** Go's `ReadMsgUnix`
   passes `MSG_CMSG_CLOEXEC` on Linux, but on FreeBSD sets close-on-exec only
   after the receive, which a concurrent spawn could race. The FreeBSD read is
   `syscall.Recvmsg` with `MSG_CMSG_CLOEXEC` under `RawConn.Read`, the
   runtime's poller waiting for the socket. The control buffer holds the
   largest `SCM_CREDS2`, 253 descriptors and a sender's own `SCM_CREDS`;
   truncated control data is refused with every descriptor closed.
3. **`MAINPIDFD=1`** takes a process descriptor — what `pdfork(2)` gives,
   FreeBSD having no pidfd — and its PID from `pdgetpid(2)`; any other
   descriptor is refused. There is no `SenderPIDFD`: FreeBSD has no
   `SO_PASSPIDFD`.
4. **The rows of ADR 0018's verdict**, restated:

   | Primitive | Linux | Darwin | FreeBSD | OpenBSD / NetBSD / DragonFly | illumos / Solaris | Windows |
   |---|---|---|---|---|---|---|
   | notify `Recv` with descriptors | ✅ (`SCM_PIDFD` ≥ 6.5) | ❌ | ✅¹ `LOCAL_CREDS_PERSISTENT` (FreeBSD 13) | ❌ | ❌ | ❌ |

   ¹ never run on the authoring host. The table of ADR 0018's own amendment
   of 2026-11-04 (`proc/systemd/notify` — `Listen`) is restated the same way
   there.
5. **Never run.** The authoring host is Linux. What ran is the decoding of
   `struct sockcred2` from a structure laid out member by member after
   sys/socket.h (`bsdabi`), the listener's shared code over Linux's
   credentials, and the compilation and `go vet` of every target. The FreeBSD
   tests — the option read back, the sender of two datagrams, a datagram
   without credentials refused with its descriptors closed, the shared
   transport suite (descriptors stored, closed when nothing calls for them or
   when refused, the barrier, a pipe refused as `MAINPIDFD`, `ListenFor`, three
   hundred cycles without a leak) — run on the e2e-cross FreeBSD 15.0 lane.
   The acceptance of a real process descriptor is not tested: the stdlib does
   not reach `pdfork(2)`.
