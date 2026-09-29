# internal/service/ipc/

## Purpose

A private socket between processes of one machine (ADR 0148): a listener only
its own account — and the accounts it names — can reach, a client that refuses
a socket another account planted, and the kernel's word on the peer where the
kernel gives it. Public facade: `pkg/v1/ipc`. Consumers: the framework's
daemon profile and listeners (`framework/kit`), the telemetry exporter's
socket, the statusline daemon.

Stdlib only (`net`, `syscall` for `SO_PEERCRED` and `Stat_t`; on Windows
`syscall.NewLazyDLL` for the named pipe's kernel32/advapi32 entry points, no
`x/sys`). Code range
`0.3.91.*`.

## Contents

| File | Role |
|---|---|
| `ipc.go` | package doc, `Config`, `PeerValue` (`SID` on Windows), `Conn`, `Listener` over an `acceptor`, `NewListener`, `Accept` (refused peers closed and counted), `Dial`, `RuntimeDir`, `admit`, `closeBestEffort` |
| `socket.go` (`!windows`) | the Unix socket: `listen` (dial-then-remove of a leftover socket, `0600`), `socketAcceptor`, `admits`, `dial` (directory and owner checked before a byte is sent, within one second) |
| `pipe_windows.go` | the named pipe (ADR 0148 §3): `pipeName`, `pipeAcceptor` (DACL `D:P(A;;GA;;;<SID>)`, `PIPE_REJECT_REMOTE_CLIENTS`, `FILE_FLAG_FIRST_PIPE_INSTANCE`, next instance before a connection is handed out, `Close` cancels a waiting `ConnectNamedPipe`), `clientPeer`/`accountOf` (process token → SID), `admits`, `dial` (`SECURITY_IDENTIFICATION`, server of another account refused) |
| `dir_unix.go` / `dir_other.go` | `prepareDir`, `checkDir`, `ownerOf`: mode and owner on Unix, a refusal elsewhere |
| `peer_linux.go` / `peer_other.go` (`!linux && !windows`) | `peerOf`: `SO_PEERCRED`, or an unverified peer |
| `codes.go` / `errors.go` | `0.3.91.1`–`8`: `MISCONFIGURED`, `DIRECTORY_UNSAFE`, `IN_USE`, `LISTEN_FAILED`, `PEER_REFUSED`, `DIAL_FAILED`, `ENDPOINT_FOREIGN`, `CLOSED` |

## Why-this-shape

- **No core package.** One engine, no port a second implementation would
  satisfy: the values are the engine's (ADR 0074), as `redact`'s are.
- **The directory is the gate everywhere; the peer's credentials are a second
  one where they exist.** Connecting to a Unix socket needs search permission
  on its directory, so a 0700 directory already keeps other accounts out; on
  Linux `SO_PEERCRED` then names the peer and the allow-lists apply. Outside
  Linux the standard library exposes no peer credential and `getpeereid` needs
  `x/sys`, which the SDK bans — `Peer.Verified` says which world a caller is in.
- **Group search is allowed, group write is not.** A deployment that admits an
  on-call group gives the directory `0750` and its group; nobody but the owner
  can plant an entry. This package never widens a directory.
- **Dial, then remove.** A socket at the path is dialled first: one that
  answers belongs to a live process and is refused (`IN_USE`), one that does
  not is a leftover and is removed — only when it is a socket this account
  owns. A regular file there is never touched.

## Do NOT

- Branch on the errno of a failed dial: the cause travels as a field, the
  verdict is `DIAL_FAILED`.
- Quote a path in a Public sentence: paths go in the `path` field.
- Widen a directory's mode or ACL.

## Verification

```sh
GOWORK=off go -C internal/service test -race ./ipc/
bazel test //internal/service/ipc:ipc_test
GOWORK=off GOOS=windows go -C internal/service vet ./ipc/   # the pipe's tests run in e2e-cross.yml's Windows lane
```
