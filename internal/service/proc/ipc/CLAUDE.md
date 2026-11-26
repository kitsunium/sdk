<!-- updated: 2026-11-13T00:00:00Z -->
# internal/service/proc/ipc/

> Reference moved to `internal/service/proc/ipc/NOTES.md` (ADR 0253), under the same section titles: Why-this-shape.

## Purpose

A private socket between processes of one machine (ADR 0148): a listener only
its own account — and the accounts it names — can reach, a client that refuses
a socket another account planted, and the kernel's word on the peer where the
kernel gives it. Public facade: `pkg/v1/proc/ipc`. Consumers: the framework's
daemon profile and listeners (`framework/app`), the telemetry exporter's
socket, the statusline daemon.

Stdlib only (`net`, `syscall` for `SO_PEERCRED` and `Stat_t`; on Windows
`syscall.NewLazyDLL` for the named pipe's kernel32/advapi32 entry points, no
`x/sys`) plus the kernel's `pathchain` for the path above the socket's
directory. The contract is `internal/core/proc/ipc`, at the mirrored path and
imported as `coreipc` (ADR 0160): `PeerValue`, `Conn`, the `Listener` and
`Dialer` ports this package's engines implement, and the `0.3.91.*` codes and
sentinels — this package returns them and declares none.

## Contents

| File | Role |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `ipc.go` | `Config` (the engine's, ADR 0074; `AllowSIDs` checked by `validate` through the platform's `checkSIDs`, #1349), `Listener` over an `acceptor` — the engine behind `coreipc.Listener`, whose `accept` returns the platform's `credentials` beside the peer —, `NewListener`, `Accept` (refused peers closed and counted), `Dialer` + `NewDialer` — the engine behind `coreipc.Dialer`, its allow-lists copied —, `Dial`, `RuntimeDir` and `RuntimeDirOr` (the same chain ending on the caller's directory instead of `<tmp>/<app>-<uid>`, ADR 0182), `admit`, `closeBestEffort` |
| `ipc_compliance.go` | the compile-time proof that `*Listener` satisfies `coreipc.Listener` and `*Dialer` satisfies `coreipc.Dialer` — a method renamed or retyped on either engine fails the build here, not in a caller holding the port |
| `socket.go` (`!windows`) | the Unix socket: `credentials` (the peer's supplementary groups), `checkSIDs` (a non-empty `AllowSIDs` is `MISCONFIGURED`, #1349), `socketPosture` (the mode and group the lists need, #1092), `checkReach` (a `0666` socket under a directory anybody searches refused where the kernel names no peer), `listen` (dial-then-remove of a leftover socket — `nobodyListens`: refused or gone, nothing else —, then `bindSocket` and `listenBound`), `socketAcceptor` (reads the peer's groups when `AllowGIDs` is set), `admits`, `dial` (directory, path and owner checked before a byte is sent, within one second) |
| `bind_unix.go` | `bindSocket` (socket, bind, then `setPosture`: the group, then the mode — before any listen), `listenBound` (`listen(2)`, `net.FileListener`, unlink on close), `listenFailed` |
| `pipe_windows.go` | the named pipe (ADR 0148 §3): `pipeName` (the account's SID in the name, #1318), `listenAs` (the account and its canonical `AllowSIDs`), `taken`/`holder`/`ownerOf` (a name that exists judged by its owner: `IN_USE` or `ENDPOINT_SQUATTED`), `pipeAcceptor` (descriptor `pipeSDDL`, `PIPE_REJECT_REMOTE_CLIENTS`, `FILE_FLAG_FIRST_PIPE_INSTANCE`, next instance before a connection is handed out, `Close` cancels a waiting `ConnectNamedPipe`), `clientPeer` (the pid, and the identity from the client's token through the pipe), `accountOf` (a process token → SID), `admits` (an unreadable token refused), `dial`/`dialAs`/`dialOrder` (`SECURITY_IDENTIFICATION`, `clientAccess`, this account's pipe then each listed SID's), `serverPeer` (the server's process token; a listed account's pipe also by its owner); `pipeConn` keeps its peer and, for a named one, a handle on its process (`newPipeConn`, `openPeerProcess` — a process younger than the connection refused —, closed by `Close`) |
| `sids_windows.go` | `AllowSIDs` on the pipe (#1349): `canonicalSIDs` (`ConvertStringSidToSid` and back, once each; Everyone, Anonymous Logon and the Null SID refused — `standsForEveryCaller`), `pipeSDDL` (`O:<SID>D:P(A;;GA;;;<SID>)` and `(A;;0x12008b;;;<listed>)`), `clientAccess`, `tokenIdentity` + `identityOf`/`tokenSIDs`/`tokenInfo` (user, enabled groups, restricting SIDs) and `admittedBy` (the kernel's two passes), `clientIdentity`/`clientToken` (`ImpersonateNamedPipeClient`, `OpenThreadToken` as self, `RevertToSelf` on a goroutine locked to its thread) |
| `files.go` (`unix \|\| windows`), `files_unix.go`, `files_windows.go`, `files_other.go` | file passing (#936): `fdPassRefused`; `SCM_RIGHTS` on Unix (`fileConn`); a frame and `DuplicateHandle` out of the sender's process on Windows, a socket by `WSADuplicateSocketW` and `WSASocketW` (`SendFiles`, `prepare`, `fileFrame`, `RecvFiles`, `readEntries`, `take`); none elsewhere |
| `dir_unix.go` / `dir_other.go` | `prepareDir` (path audited before AND after the `Mkdir`), `checkDir` (path above, own entry, holder), `checkEntry` (mode, owner, not a link), `ownerOf`: on Unix; a refusal elsewhere |
| `chain_unix.go` | `checkChain` over `pathchain.Resolve` of the socket directory's parent, `checkHolder`, `steerable` (the rule), `ours`, `pathUnsafe` — see §Why-this-shape |
| `peer_linux.go` / `peer_other.go` (`!linux && !windows`) | `peerOf`: `SO_PEERCRED`, or an unverified peer |
| `peergroups_linux.go`, `sockopt_linux.go` / `sockopt_linux_386.go` | `peerGroups`: `SO_PEERGROUPS` through `getsockopt(2)` — its own syscall on i386 (365), where the syscall package has only `socketcall` |
| `peer_bsd.go`, `peer_xucred.go`, `peer_unpcbid.go` | `peerOf` and `peerGroups` over one `peerBuffer`: the `xucred`'s uid, groups and pid (FreeBSD, DragonFly), the `unpcbid`'s uid, egid and pid (NetBSD, no groups) |

The codes — `0.3.91.1`–`11`: `MISCONFIGURED`, `DIRECTORY_UNSAFE`, `IN_USE`,
`LISTEN_FAILED`, `PEER_REFUSED`, `DIAL_FAILED`, `ENDPOINT_FOREIGN`, `CLOSED`,
`PATH_UNSAFE`, `FD_PASS_REFUSED`, `ENDPOINT_SQUATTED` — are `internal/core/proc/ipc`'s `codes.go` and `errors.go`.

## The socket directory's rule, beside the other four

`app/lock`, `security/secret`, `security/session` and `data/queue` each refuse
a directory by a rule of their own, compared side by side in
`internal/kernel/fs/CLAUDE.md` §Five directory rules. They share the kernel's
`pathchain` walk and no rule; this one's, and why it is not a neighbour's:

- **The directory is the whole access control**, so it must be this account's
  and writable by nobody else: a link, another account's directory, or a
  group- or other-WRITABLE one (`0o022`) is `DIRECTORY_UNSAFE`. `lock` and
  `queue` accept a group-writable directory because they are shared on
  purpose; a private socket is not.
- **Group SEARCH is allowed** (`0750`), where `secret` and `session` refuse any
  group bit: an on-call group reaching this directory reaches a socket whose
  listener admits only the accounts it names (and, on Linux, checks the peer),
  while reading theirs reaches the secrets themselves.
- **The path above is judged three ways** wherever anybody can write the
  holder — an indirection, a component neither this account's nor root's, or
  any component without sticky (`PATH_UNSAFE`) — where `lock` and `session`
  refuse only the first. The OWNER rule is this package's alone: a component
  another account owns decides everything below it, and a lock that is shared
  between accounts on purpose cannot ask it.
- **It runs on both sides**: at `Listen`, before and after the `Mkdir`, and at
  every `Dial` — the only rule of the five a client applies too.
- **`Mkdir`, never `MkdirAll`**: the parent must already exist and is audited
  first, so nothing is ever created inside a steered tree.
- **No DACL is READ on Windows**: there is no directory there. The named pipe
  carries a DACL this package BUILDS (`D:P(A;;GA;;;<SID>)` and, since #1349, an
  entry `(A;;0x12008b;;;<SID>)` per listed SID, ADR 0148 §3), so the kernel's
  reader, `internal/kernel/fs/winacl`, is not this package's.

## Do NOT

- Branch on the errno of a failed dial: the cause travels as a field, the
  verdict is `DIAL_FAILED`.
- Quote a path in a Public sentence: paths go in the `path` field.
- Widen a directory's mode or ACL.
- Refuse a link above the directory for being a link: macOS's `/tmp` is one.
  The verdict is the link AND a holder anybody can write.
- Exempt a component for its holder's sticky bit when judging a link or an
  owner: sticky governs unlinking an entry that exists, not creating one.
- Re-read an owner with a second `Lstat` of `StepValue.Path`: the walk already
  read it relative to the handle it held.

## Verification

```sh
GOWORK=off go -C internal/service test -race ./proc/ipc/
bazel test //internal/service/proc/ipc:ipc_test
GOWORK=off GOOS=windows go -C internal/service vet ./proc/ipc/   # the pipe's tests run in e2e-cross.yml's Windows lane
```

`chain_external_test.go` pins every row of the rule on both sides (Listen and
Dial); `TestADirectoryAnotherAccountCreatedAboveTheSocketIsRefused` and the
`/var/run` row need root and skip, saying so, elsewhere — they run in a root
container or a CI job (`TestSteerable` pins the owner rule unprivileged).
