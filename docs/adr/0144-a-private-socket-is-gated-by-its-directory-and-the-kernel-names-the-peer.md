# ADR 0144 — a private socket is gated by its directory, and the kernel names the peer where it can

- **Status**: Proposed
- **Date**: 2026-09-28
- **Deciders**: SDK maintainers
- **Related**: [ADR 0143](0143-the-framework-is-a-module-of-the-sdk-above-pkg.md) (the framework's daemon profile and listeners are its first consumer), [ADR 0052](0052-sdk-lock-domain.md) / [ADR 0083](0083-a-path-is-a-chain-and-a-held-lock-can-lose-its-file.md) (a directory another account can write to is refused), [ADR 0081](0081-the-windows-file-lock-is-a-different-primitive.md) / [ADR 0086](0086-creating-an-entry-is-not-replacing-one-and-windows-says-so-in-two-bits.md) (Windows entry points bound with `syscall.NewLazyDLL`, no `x/sys`), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (no core package), [ADR 0094](0094-a-test-compiles-where-its-package-does.md) (the 104-byte `sun_path`)

## Context

Three consumers need the same thing within weeks of each other: a daemon
that short-lived clients of the same user talk to (the statusline test bench,
D5 of the platform's plan), a framework role that listens on something other
than HTTP (ADR 0143 §5, V-A), and a telemetry exporter an operator's tool
attaches to (the plan's §6). Each needs a socket nobody else on the machine can
use, and each has the same two failure modes: a listener that hands requests to
anybody who can reach a path, and a client that sends its request — a token, a
prompt — to whatever socket sits at the path it expected, including one another
account planted there first.

## Decision

A new service package, `internal/service/ipc`, with the facade `pkg/v1/ipc`,
code range `0.3.90.*`. No core package: one engine and no port (ADR 0074).

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
