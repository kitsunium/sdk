<!-- updated: 2026-11-08T00:00:00Z -->
# internal/service/proc/systemd/notify/

## Purpose

Concrete implementation of the **sd_notify(3)** readiness/watchdog protocol — both
the notifier (child) and listener (supervisor) sides — over the `Listener` port
and `NotificationValue` value type declared in `internal/core/proc`. Backs the
`pkg/v1/proc/systemd/notify` facade.

The package is `notify` — `sdnotify` until ADR 0155 moved it under
`proc/systemd/`; its files keep the protocol's name (`sdnotify.go`). Importers
in this module write `notify.ReadyContext` (health) and `notify.Ready`
(lifecycle); the facade imports it as `svcnotify`.

## Surface

| Symbol | Side | Platform |
|---|---|---|
| `Notify(state)` / `Ready` / `Reloading` / `Stopping` / `Status` / `Watchdog` / `MainPID` | notifier | Unix (no-op when unset); on Windows a no-op when unset and `UnsupportedPlatform` when `NOTIFY_SOCKET` is named — `AF_UNIX` there has no `SOCK_DGRAM` (`dial_windows.go`, #941) |
| `NotifyContext(ctx, state)` / `ReadyContext` / `StatusContext` | notifier, bounded | as `Notify` |
| `WatchdogInterval()` | env query | all |
| `Listen() (Listener, path, err)` | supervisor | linux, freebsd |
| `ListenFor(uid, gid) (Listener, path, err)` | supervisor, the socket handed to a service's user (#730) | linux, freebsd |
| `Detach(l) (file, err)` / `Adopt(f, path) (Listener, err)` | supervisor, the listening socket handed to another image at the same path (#738, S0h) | linux, freebsd |

## Protocol semantics (libsystemd-faithful)

- **`$NOTIFY_SOCKET` unset ⇒ no-op.** Every notifier function returns `nil` and
  sends nothing when `NOTIFY_SOCKET` is unset or empty. This is the documented
  sd_notify behaviour so an unsupervised binary stays silent rather than erroring.
- **Set-but-unreachable ⇒ `NotifyFailed`.** A dial/write failure against a
  configured socket is a real error (`coreproc.CodeNotifyFailed`).
- **The write is the only unbounded step, and the context siblings bound it**
  (ADR 0072). A unixgram write blocks once the RECEIVER's queue is full, and
  the receiver is the supervisor: measured on Linux, a brand new sender — which
  is what every send here is — parks at the 514th small datagram. `Notify` has
  no deadline and says so; `NotifyContext` hands ctx's deadline to the kernel as
  the socket's write deadline and reaches a write already parked through
  `context.AfterFunc`, which is the only way to interrupt a `net.Conn`. It
  bounds the WRITE and not the dial, since a unixgram dial takes no round trip.
  A write the bound ends is `NotifyFailed` over the socket's error joined with
  ctx's, so an expiry reads `os.ErrDeadlineExceeded` and a cancellation also
  answers `errors.Is(err, context.Canceled)`.
  Only the two helpers with a caller that has a deadline to inherit have a
  sibling; the others gain one when something needs it.

  Its tests are **Linux-only** (`notify_bounded_linux_external_test.go`), and
  that is a property of the HARNESS, not of the bound: making a send block means
  filling the supervisor's receive queue, which is Linux's mechanism. CI showed
  the alternatives on the first run — FreeBSD accepted all 8192 fillers without
  ever blocking a sender, and macOS refused the send outright instead of parking
  it, so no deadline had anything to interrupt. Weakening the assertions to
  something all three satisfy would have kept them green while testing nothing.
- **Abstract namespace.** A `$NOTIFY_SOCKET` value beginning with `@` selects the
  Linux abstract namespace; the `@` is replaced by a NUL byte in `sun_path[0]`
  before binding/connecting (`resolveAddr`).
- **Datagram body.** Newline-separated `NAME=value` fields (`READY=1`,
  `RELOADING=1`, `STOPPING=1`, `STATUS=…`, `MAINPID=…`, `WATCHDOG=1`). `STATUS`
  and `MAINPID` are lifted into the typed `NotificationValue` fields; a non-`=`
  line or a non-integer `MAINPID` yields `InvalidNotification`.
- **`WATCHDOG_USEC`.** Parsed as a base-10 microsecond count into a
  `time.Duration`; unset/garbage/zero ⇒ `ok=false`, mirroring
  `sd_watchdog_enabled`. Two things `sd_watchdog_enabled(3)` does that this
  once did not (#451): a set **`WATCHDOG_PID`** must name `getpid()` — a child
  inheriting its parent's environment is not the watched process, and a
  malformed PID addresses nobody — and a count above `math.MaxInt64 / 1000`
  (`maxWatchdogUsec`) is refused: `time.Duration(usec) * time.Microsecond`
  wrapped, `9223372036854775807` read as `-1µs` with `ok=true`, and the
  package example's `time.NewTicker(d/2)` panicked. `ok=true` now promises a
  positive `d`.

## Performance — see `BENCH.md`

A `Ready()` / `Watchdog()` call is **0.7 % formatting and 99.3 % socket**
(168.8 ns to build `READY=1\n` and validate it, inside a 23.7 µs
dial/write/close). An UNSUPERVISED binary — `$NOTIFY_SOCKET` unset — pays
**83.7 ns and zero allocations** per call, so the shorthands can be shipped
unconditionally. There is no watchdog interval a supervisor would plausibly
configure at which this package becomes a cost.

Two consequences are pinned in the code:

- The field-injection check uses `strings.Contains(name, "\n") ||
  strings.Contains(name, "=")` rather than `strings.ContainsAny(name, "\n=")`.
  Both delimiters are ASCII and a UTF-8 continuation byte is never `0x0A` or
  `0x3D`, so the results are identical for every input; `ContainsAny` decodes a
  rune per byte for a name of ≤8 bytes (every sd_notify field name), which a
  profile named at 34 % of `encodePayload`. Worth **−24 %** on the function and
  −0.2 % on the call — kept because it is free, not because it matters.
- The `strings.Builder` is deliberately **not** pre-sized. Computing the exact
  size needs a second `range` over the map, and a map range costs ~60 ns of
  iterator setup — measured at **+29 % on `Ready()`**, which is a one-entry map
  like every other shorthand. The comment that used to claim pre-sizing now
  states the measurement instead.

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

## Credential verification (SO_PASSCRED)

`Listen` binds a `unixgram` socket under a private `0700` temp dir and sets
`SO_PASSCRED` via `conn.SyscallConn()` + `syscall.SetsockoptInt`. `Recv` reads
with `ReadMsgUnix`, parses the OOB control data with
`syscall.ParseSocketControlMessage` + `syscall.ParseUnixCredentials`, and fills
`NotificationValue.SenderPID` with the **kernel-verified** sender PID — a value
the sender cannot forge. The temp dir (and socket inode) is removed on `Close`.

## Layering / platform split

- Imports stdlib (`bytes`, `context`, `encoding/binary`, `net`, `os`,
  `syscall`, `strings`, `strconv`, `time`, `errors`, `path/filepath`,
  `unicode/utf8`) + `internal/core/proc` + `internal/kernel/errs`.
  Never imports `pkg/*` or `golang.org/x/sys`.
- `notify.go` + `sdnotify.go` + `assignments.go` are portable (no build tag).
- `listen_unix.go` (`//go:build linux || freebsd`) holds the
  credential-passing listener; `listen_linux.go` and `listen_freebsd.go` what
  differs — the socket option, the read, the control messages, a pidfd's PID;
  `listen_other.go` (`//go:build !linux && !freebsd`) returns
  `coreproc.UnsupportedPlatform` from `Listen`. The package compiles on every
  GOOS.

## Errors

No new codes are defined here. The package wraps the central `core/proc`
sentinels: `NotifyFailed`, `ListenFailed`, `InvalidNotification`,
`UnsupportedPlatform` (and is wired to restate `CredentialMismatch` if a future
caller layers PID-pinning on top of `SenderPID`). Match with
`errs.HasCode(err, coreproc.CodeX)`.

## Verification

```sh
go test -race ./...
ktn-linter lint ./...
```

The credential round-trip (`TestListenRoundTrip`) works in an unprivileged
container (same-process `SO_PASSCRED` send). The Linux listener tests fail,
never skip, when `Listen` cannot bind; off Linux and FreeBSD,
`TestListenUnsupportedOffLinuxTagged` (`sdnotify_other_test.go`,
`!linux && !freebsd`)
asserts the typed `UnsupportedPlatform` with no listener and no path.
