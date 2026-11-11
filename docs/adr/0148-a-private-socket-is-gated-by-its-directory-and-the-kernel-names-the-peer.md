# ADR 0148 — a private socket is gated by its directory, and the kernel names the peer where it can

- **Status**: Accepted (proposed 2026-09-28; accepted 2026-10-03, once the code it decides had shipped)
- **Date**: 2026-09-28
- **Deciders**: SDK maintainers
- **Amended by**: [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) — `ipc` gains a core, with the ports a test double needs; [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md) §As built — the gate is the whole path: where anybody can write the directory holding a component above the socket's directory, a link, a component neither this account's nor root's, or — when that directory has no sticky bit — any component is refused (`PATH_UNSAFE`, `0.3.91.9`)
- **Related**: [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (the framework's daemon profile and listeners are its first consumer), [ADR 0052](0052-sdk-lock-domain.md) / [ADR 0083](0083-a-path-is-a-chain-and-a-held-lock-can-lose-its-file.md) (a directory another account can write to is refused), [ADR 0081](0081-the-windows-file-lock-is-a-different-primitive.md) / [ADR 0086](0086-creating-an-entry-is-not-replacing-one-and-windows-says-so-in-two-bits.md) (Windows entry points bound with `syscall.NewLazyDLL`, no `x/sys`), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (no core package), [ADR 0094](0094-a-test-compiles-where-its-package-does.md) (the 104-byte `sun_path`)

## Context

Three consumers need the same thing within weeks of each other: a daemon
that short-lived clients of the same user talk to (the statusline test bench,
D5 of the platform's plan), a framework role that listens on something other
than HTTP (ADR 0147 §5, V-A), and a telemetry exporter an operator's tool
attaches to (the plan's §6). Each needs a socket nobody else on the machine can
use, and each has the same two failure modes: a listener that hands requests to
anybody who can reach a path, and a client that sends its request — a token, a
prompt — to whatever socket sits at the path it expected, including one another
account planted there first.

## Decision

A new service package, `internal/service/ipc`, with the facade `pkg/v1/ipc`,
code range `0.3.91.*`. No core package: one engine and no port (ADR 0074).

1. **The directory is the gate, everywhere.** A socket lives in a directory
   its account owns and nobody else can write to — created `0700` when missing,
   refused otherwise (`DIRECTORY_UNSAFE`, `EX_CONFIG`): a link, not a
   directory, another owner, group- or world-writable. The socket is `0600`.
   Connecting to a Unix socket needs search permission on its directory, so the
   directory alone keeps other accounts out. Group search is allowed — that is
   how a deployment admits an on-call group — and this package never widens a
   directory. Windows has no search permission to make a directory a gate:
   there the endpoint is a named pipe (point 6).
2. **The kernel names the peer where it can.** On Linux `SO_PEERCRED` gives the
   peer's UID, GID and PID; a peer that is neither this account nor in
   `AllowUIDs`/`AllowGIDs` is closed before `Accept` returns and counted
   (`Refused`). The dialer checks the listener the same way. Elsewhere the
   standard library exposes no credential and `getpeereid`/`LOCAL_PEERCRED`
   need `x/sys`, which the SDK bans: `Peer.Verified` is false and says so.
3. **Nothing is taken over.** `Listen` dials a socket already at the path:
   one that answers is refused (`IN_USE`) and left alone, one that does not —
   what a killed daemon leaves — is removed, provided it is a socket this
   account owns; anything else at the path is refused and never touched.
4. **A client checks before it speaks.** `Dial` refuses an unsafe directory
   and a socket file another account owns (`ENDPOINT_FOREIGN`) before a byte is
   sent.
5. **`RuntimeDir`** names where an application's sockets belong:
   `$RUNTIME_DIRECTORY`, `$XDG_RUNTIME_DIR/<app>`, `%LOCALAPPDATA%\<app>`,
   else `<tmp>/<app>-<uid>`. Paths are at most 103 bytes.

A process singleton needs nothing new: `pkg/v1/lock`'s file locker is
`flock`/`LockFileEx` behind a checked directory, and the framework's roles use
it as it is.

## Consequences / Semantics

- A daemon and its clients share one `Config` value; `IN_USE` at start means
  "a daemon runs — be its client", `DIAL_FAILED` means "nobody answers — start
  one".
- On macOS and the BSDs the allow-lists are not enforced by the kernel's word,
  only by the directory; the documentation says so in three places.

## Breaking changes

None: a new package.

## Alternatives considered

- **A TCP port on loopback with a token.** Refused: any process of any account
  can connect to loopback, and the token then has to be stored somewhere those
  accounts cannot read — which is a private directory again.
- **Abstract Unix sockets (Linux).** Refused: they have no filesystem
  permission at all, so the directory gate would vanish.
- **`AF_UNIX` on Windows behind the directory's DACL.** The first version of
  this ADR did that, reading the directory with the lock domain's DACL reader.
  Replaced before release: Windows gives an `AF_UNIX` peer no credential, and
  a directory's list is only a refusal at `Listen` and `Dial` time — a named
  pipe's DACL is enforced by the object manager on every open, names the
  account rather than trusting a directory, and its process ids give each end
  the other's token.
- **Refusing to run where the peer cannot be verified.** Refused: macOS is a
  primary platform of the first consumer, and the directory is a sound gate
  there.

## Windows: a named pipe with its own DACL

6. **On Windows the endpoint is a named pipe**, named after `Config.Path`
   (`\\.\pipe\ipc-<16 hex digits of the path's SHA-256>-<base name>`, so one
   configuration serves every platform; nothing is created in the directory):
   - its DACL grants this process's account only, protected from
     inheritance (`D:P(A;;GA;;;<SID>)`);
   - `PIPE_REJECT_REMOTE_CLIENTS` refuses another machine's client;
   - the first instance is created with `FILE_FLAG_FIRST_PIPE_INSTANCE`, so a
     name that exists — a live daemon, or a squatter that made it first — is
     `IN_USE`, never joined; the next instance is created before a connection
     is handed out, so the name does not lapse while the listener lives;
   - each end reads the other's account from its process token
     (`GetNamedPipeClientProcessId`/`GetNamedPipeServerProcessId`, then
     `OpenProcessToken`) into `Peer.SID`, `Peer.Verified` true: the listener
     refuses another account (`PEER_REFUSED`), the client refuses a server of
     another account, or one whose account it cannot read, before it sends a
     byte (`ENDPOINT_FOREIGN`, `DIAL_FAILED`), and opens the pipe at
     `SECURITY_IDENTIFICATION` so the server can never act as it;
   - `AllowUIDs`/`AllowGIDs` admit nobody more: Windows has no UID, and the
     DACL names this account only.

   The handles are overlapped and handed to `os.NewFile`, which puts them on
   the runtime's poller (Go 1.27 detects an overlapped handle): reads, writes
   and deadlines are the `os` package's. `CreateNamedPipeW`,
   `ConnectNamedPipe`, `GetNamedPipe*ProcessId`, `CreateEventW`,
   `GetOverlappedResult` and
   `ConvertStringSecurityDescriptorToSecurityDescriptorW` are bound with
   `syscall.NewLazyDLL`, as ADR 0081/0086 bound the lock domain's: `x/sys`
   stays banned (ADR 0018). The runtime proof is the `ipc` entry of the
   Windows lane of `e2e-cross.yml` (`pipe_windows_external_test.go`).

## Deferred

- `getpeereid` on the BSDs and macOS without `x/sys`.

## Verification

```sh
GOWORK=off go -C internal/service test -race ./ipc/
bazel test //internal/service/ipc:ipc_test //pkg/v1/ipc:ipc_test
```

## References

- `unix(7)` — "connecting to the socket object requires write permission on
  the socket"; search permission on the directories above it.
- `SO_PEERCRED`, `socket(7)`.

## Amendment — 2026-11-04: the dialling side, the BSDs, and the unnamed peer (kitsunium/sdk#1092, #1093)

1. **The directory rule is split by side.** At Listen the socket directory
   is this account's, unchanged. At Dial it is the LISTENER's, usually another
   account's: its entry rules hold (a real directory writable by its owner
   alone), its owner is not judged. The socket file's owner is: this account,
   an `AllowUIDs` member or an `AllowGIDs` group, `ENDPOINT_FOREIGN` otherwise.
   Before, Dial refused every directory another account owned
   (`DIRECTORY_UNSAFE`), so the deployment §Decision describes — the daemon's
   0750 directory, its on-call group in `AllowGIDs` — could not be dialled by
   the group it admits, nor by root.
2. **§Deferred narrowed.** FreeBSD and DragonFly answer
   `getsockopt(SOL_LOCAL, LOCAL_PEERCRED)` with `struct xucred`, NetBSD
   `LOCAL_PEEREID` with `struct unpcbid`, both reachable with
   `syscall.Syscall6(SYS_GETSOCKOPT, …)` as `rctl` and `procctl` already are:
   the peer is named there. darwin (raw syscalls deprecated), OpenBSD (pinned
   syscalls) and illumos/Solaris (`getpeerucred` is libc's) stay deferred.
3. **An unnamed peer is refused where the kernel names peers.** On Linux and
   the three BSDs a peer the kernel did not name — a failed getsockopt — is
   `PEER_REFUSED` with the reason, where it used to be admitted as if the
   directory had been the gate (ADR 0031). Windows keeps its DACL rule.

## Amendment — 2026-11-08: files over the Windows pipe (kitsunium/sdk#936)

`FileSender` and `FileReceiver` (`FD_PASS_REFUSED`, `0.3.91.10`) shipped on
Unix with `SCM_RIGHTS`; the pipe now offers them too. A pipe carries bytes,
so the files travel in band, in a frame the receiver must read with
`RecvFiles`. The issue asked the sender to `DuplicateHandle` into the
receiver; the receiver takes each handle out of the sender's process
instead (`DUPLICATE_CLOSE_SOURCE`), so it never wraps a value it was only
told — pushed, a peer unable to open the receiver could name a handle the
receiver already holds. The handle on the peer's process is opened when the
connection completes and a process younger than the connection is refused,
so a pid reused meanwhile is never reached. Still refused to a peer the
kernel did not name. A TCP listener cannot be passed there: the standard
library gives a Windows socket no `*os.File`.

## Amendment — 2026-11-09: a pipe name another account holds (kitsunium/sdk#1318)

Point 6 named the pipe after the path alone, in a namespace every account of
the machine shares, and read a name that exists as `IN_USE`, "whoever made
it". Another account could make it first and the listener then reported our
own daemon running for as long as the squatter held the name.

1. **The name carries the account**:
   `\\.\pipe\ipc-<SID>-<16 hex digits of the path's SHA-256>-<base name>`, the
   SID of the listening (or dialling) process. Two accounts with one path no
   longer meet on one name; `Dial` reaches its own account's pipe only, which
   is the only one point 6 ever let it trust.
2. **The pipe is owned by that account** (`O:<SID>D:P(A;;GA;;;<SID>)`), not by
   the token's default owner, which is Administrators for an elevated process.
3. **A name that exists is judged by its holder.** One more instance is
   created without `FILE_FLAG_FIRST_PIPE_INSTANCE`, its owner read with
   `GetSecurityInfo` and the instance closed at once: this account's pipe is
   `IN_USE`; one another account owns, or one whose DACL admits no instance
   of ours, is `ENDPOINT_SQUATTED` (`0.3.91.11`, exit 78), a code of its own
   because the remedy is a human finding the holder, not talking to it.
4. **What it does not do**: prevent the squat. A SID is no secret, so the name
   stays computable; Windows offers no per-account pipe namespace outside an
   AppContainer. The squat is named, never mistaken for a daemon of ours, and
   a client still refuses a server of another account before it sends a byte
   (`ENDPOINT_FOREIGN`). The probe instance listens for the moment it lives,
   and a pipe gone between the two creates is re-made by the probe and read
   as ours (`IN_USE`, cleared by a retry).

## Amendment — 2026-11-11: the socket is opened to the accounts the lists name (kitsunium/sdk#1092)

The 2026-11-04 amendment let a client dial a directory another account owns,
and the allow-lists still admitted no client of another account but root: the
socket was `0600`, and `connect(2)` needs write permission on it, so the
kernel refused every account the lists named before the listener saw it. And
the admission compared the peer's effective group alone, so the member of an
on-call group — a supplementary group, as such a group is — was refused even
once it got through. The owner decided on 2026-11-10 (option a of the issue)
to open the socket's mode when a list is given, the directory staying the
first gate.

1. **The mode is the narrowest the lists need** (`socketPosture`): `0600`
   when they name nobody but this account and root (whom no mode stops);
   `0660` and the socket given that group when they name one group and no
   other user — the kernel's own permission check then admits the group's
   members, supplementary ones included, and nobody else, on every platform;
   `0666` when they name another user or several groups, which no single
   group can hold. Read bits grant nothing on a socket; `0660` and `0666` are
   written as service managers write them. A group the socket cannot be given
   — the listening account is not in it, and is not root — is
   `LISTEN_FAILED`: left at its old group it would admit nobody the list
   names, opened to everyone it would admit more than it names (ADR 0031).
2. **No moment where the socket is wider than its posture.** `bind(2)`
   creates the file with the mode the umask leaves (`0777` under a zero
   umask); the group and the mode are set between `bind(2)` and `listen(2)`,
   and a `connect(2)` to a socket that does not listen is refused
   (`ECONNREFUSED`) on every Unix, so no connection completes before the file
   has its posture. The umask is not touched — it is the process's, and Go
   runs threads that create files meanwhile —; Linux's `fchmod` on an unbound
   socket is not portable.
3. **The directory stays the first gate, and is never widened.** A `0666`
   socket in a directory anybody can search, where the kernel names no peer
   (darwin, OpenBSD, illumos, Solaris), would admit anybody: neither the
   directory, nor the mode, nor the admission — which admits an unnamed peer —
   would refuse. `Listen` refuses it (`DIRECTORY_UNSAFE`, rule "searchable by
   anyone…"); a directory whose group is the allowed accounts' (`0750`)
   passes. Where the kernel names peers, the admission refuses who the lists
   do not name, whatever the directory, and `Refused` counts them.
4. **A peer is admitted by its supplementary groups too**, read from the
   kernel, never from the process: Linux's `SO_PEERGROUPS` (4.13) returns the
   group list of the very credentials `SO_PEERCRED` reads, recorded when the
   peer connected or listened — `/proc/<pid>/status`, which the issue
   offered, reads a process whose pid may since name another and whose groups
   may have changed, and was not taken. FreeBSD and DragonFly read the
   `xucred`'s `cr_groups` (sixteen at most); NetBSD's `unpcbid` carries the
   effective gid alone, and there a supplementary group admits nobody. A
   kernel that does not answer leaves the peer judged on its effective group:
   fewer are admitted, never more.
5. **Windows is unchanged and has the same limit**: the pipe's DACL grants
   the listening account only, and a client refuses a server of another
   account, so the lists admit nobody more there. Opening the pipe to other
   accounts needs lists of SIDs, which `Config` does not have — another
   decision, not taken here.
6. **What it costs.** A socket opened to the lists is reachable by every
   account the directory lets through; under a world-searchable directory on
   Linux and the BSDs, every account may connect and be refused, counted, by
   the listener. The deployment that wants the others kept out at the kernel
   gives the directory the allowed accounts' group. A listener with
   `AllowGIDs` naming a group its account is not in, which started before and
   admitted only root, now fails to start.
