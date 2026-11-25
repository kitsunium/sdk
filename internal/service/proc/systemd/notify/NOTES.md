# Notes — <!-- updated: 2026-11-08T00:00:00Z -->

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles. `CLAUDE.md` keeps what to obey; this file holds the rest.

## The whole transport (#730)

`Recv` reads what sd_notify(3) can carry with `MSG_CMSG_CLOEXEC`: every
descriptor arrives close-on-exec, atomically, so no spawn in flight inherits
one — no `ForkLock` is needed for that. On Linux through `ReadMsgUnix`, which
passes the flag; on FreeBSD, where `ReadMsgUnix` sets close-on-exec only after
the fact, through `RawConn.Read` and `syscall.Recvmsg` with the flag
(`listen_freebsd.go`, `read`). The control buffer is sized for `SCM_RIGHTS`
with `SCM_MAX_FD` (253) descriptors and the credentials — `SCM_CREDENTIALS` and
`SCM_PIDFD` on Linux, the largest `SCM_CREDS2` and a sender's own `SCM_CREDS`
on FreeBSD (`oobBufSize`).

- **Refusals close everything** (`control.closeAll`): a truncated datagram
  (`MSG_TRUNC` or a full buffer), truncated control data (`MSG_CTRUNC` — the
  kernel installed only the descriptors that fit), an embedded NUL, a line
  without `=`, an empty name, an assignment out of bounds, no credentials.
  `TestRecvRefusalClosesEverything` counts `/proc/self/fd` around each.
- **Every assignment is bounded** (`assignments.go`, `assignmentRules`): the
  flags (`READY`, `RELOADING`, `STOPPING`, `FDSTORE`, `FDSTOREREMOVE`,
  `BARRIER`, `MAINPIDFD`, `RESTART_RESET`) are `1`; `MAINPID` a positive
  decimal PID; `WATCHDOG` `1` or `trigger`; the `*_USEC` counts and
  `MAINPIDFDID` unsigned decimal; `ERRNO` non-negative; `STATUS` and
  `EXIT_STATUS` valid UTF-8; `BUSERROR`/`VARLINKERROR` 1–255 printable bytes;
  `NOTIFYACCESS` one of the four; `FDNAME` systemd's `fdname_is_valid`; `FDPOLL`
  `0` or `1`. One out of bounds refuses the datagram with
  `INVALID_NOTIFICATION` naming it — stricter than systemd, which drops the one
  assignment. An unknown name is kept as it came; a name given twice keeps its
  last value. `TestAssignmentRulesCoverSdNotify` fails when the rules and the
  issue's list part.
- **The descriptors, as systemd's manager hands them** (`handOverFiles`):
  `BARRIER=1` must carry exactly one and is returned ALONE (`State` =
  `BARRIER=1`, its other assignments dropped) — the caller closes it once every
  earlier datagram is processed, which is the barrier (`TestRecvBarrier`: the
  sender's pipe sees EOF only then); `MAINPIDFD=1` takes the first as the main
  process's pidfd (`MainPIDFD`), its PID read from `/proc/self/fdinfo`
  (`MainPID`, whatever `MAINPID=` said); `FDSTORE=1` hands over the rest
  (`Files`); any descriptor nothing called for is closed. They travel in
  `NotificationValue.Descriptors` (a `*DescriptorsValue`, nil when none).
- **The sender** is `SenderPID`, `SenderUID`, `SenderGID` from
  `SO_PASSCRED`, and `SenderPIDFD` — a pidfd on the sender, beyond a PID
  reuse — from `SO_PASSPIDFD` (Linux 6.5, option 76, `SCM_PIDFD` 0x04, cited);
  an older kernel refuses the option and `SenderPIDFD` is nil. The calling
  layer judges the identity (another uid, another cgroup); this package
  exposes it.
- **`ListenFor(uid, gid)`** is `Listen` with the private directory and the
  socket `lchown`ed to the service's user, so only it and root reach the
  socket; without the privilege it is `LISTEN_FAILED` and the directory is
  removed (`TestListenFor`).
- **FreeBSD** (`listen_freebsd.go`, ADR 0219 amended 2026-11-08):
  `LOCAL_CREDS_PERSISTENT` (`SOL_LOCAL` 0, option 3, sys/un.h — hard-coded,
  the stdlib names neither) makes the kernel prepend an `SCM_CREDS2` (0x08) to
  every datagram, a `struct sockcred2` a sender cannot forge — the kernel
  refuses that type in a message sent (`unp_internalize`). It is decoded by
  `proc/internal/bsdabi.Sockcred2` (version 0, the groups it counts present,
  at most `CMGROUP_MAX`): `SenderPID`, and the REAL `SenderUID` and
  `SenderGID` — what Linux's `SCM_CREDENTIALS` carries by default. A datagram
  without it — the kernel could not allocate it, or the option is off — is
  `CREDENTIAL_MISMATCH` with its descriptors closed, as on Linux.
  `MAINPIDFD=1` takes a process descriptor (`pdfork(2)`'s), its PID read by
  `pdgetpid(2)`; any other descriptor is refused. No `SenderPIDFD`: FreeBSD
  has no `SO_PASSPIDFD`. Never run on the authoring host: the shared
  `transport_internal_test.go` and `transport_freebsd_internal_test.go` run on
  the e2e-cross FreeBSD lane.
- **darwin, OpenBSD, NetBSD, DragonFly** stay `UNSUPPORTED_PLATFORM`, as
  #730's platform table decides (a typed refusal, no credentials relied on).
