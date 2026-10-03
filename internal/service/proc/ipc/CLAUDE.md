# internal/service/proc/ipc/

## Purpose

A private socket between processes of one machine (ADR 0148): a listener only
its own account — and the accounts it names — can reach, a client that refuses
a socket another account planted, and the kernel's word on the peer where the
kernel gives it. Public facade: `pkg/v1/proc/ipc`. Consumers: the framework's
daemon profile and listeners (`framework/kit`), the telemetry exporter's
socket, the statusline daemon.

Stdlib only (`net`, `syscall` for `SO_PEERCRED` and `Stat_t`; on Windows
`syscall.NewLazyDLL` for the named pipe's kernel32/advapi32 entry points, no
`x/sys`) plus the kernel's `pathchain` for the path above the socket's
directory. Code range `0.3.91.*`.

## Contents

| File | Role |
|---|---|
| `ipc.go` | package doc, `Config`, `PeerValue` (`SID` on Windows), `Conn`, `Listener` over an `acceptor`, `NewListener`, `Accept` (refused peers closed and counted), `Dial`, `RuntimeDir`, `admit`, `closeBestEffort` |
| `socket.go` (`!windows`) | the Unix socket: `listen` (dial-then-remove of a leftover socket, `0600`), `socketAcceptor`, `admits`, `dial` (directory, path and owner checked before a byte is sent, within one second) |
| `pipe_windows.go` | the named pipe (ADR 0148 §3): `pipeName`, `pipeAcceptor` (DACL `D:P(A;;GA;;;<SID>)`, `PIPE_REJECT_REMOTE_CLIENTS`, `FILE_FLAG_FIRST_PIPE_INSTANCE`, next instance before a connection is handed out, `Close` cancels a waiting `ConnectNamedPipe`), `clientPeer`/`accountOf` (process token → SID), `admits`, `dial` (`SECURITY_IDENTIFICATION`, server of another account refused) |
| `dir_unix.go` / `dir_other.go` | `prepareDir` (path audited before AND after the `Mkdir`), `checkDir` (path above, own entry, holder), `checkEntry` (mode, owner, not a link), `ownerOf`: on Unix; a refusal elsewhere |
| `chain_unix.go` | `checkChain` over `pathchain.Resolve` of the socket directory's parent, `checkHolder`, `steerable` (the rule), `ours`, `pathUnsafe` — see §Why-this-shape |
| `peer_linux.go` / `peer_other.go` (`!linux && !windows`) | `peerOf`: `SO_PEERCRED`, or an unverified peer |
| `codes.go` / `errors.go` | `0.3.91.1`–`9`: `MISCONFIGURED`, `DIRECTORY_UNSAFE`, `IN_USE`, `LISTEN_FAILED`, `PEER_REFUSED`, `DIAL_FAILED`, `ENDPOINT_FOREIGN`, `CLOSED`, `PATH_UNSAFE` |

## Why-this-shape

- **No core package.** One engine, no port a second implementation would
  satisfy: the values are the engine's (ADR 0074), as `redact`'s are.
- **The directory is the gate everywhere; the peer's credentials are a second
  one where they exist.** Connecting to a Unix socket needs search permission
  on its directory, so a 0700 directory already keeps other accounts out; on
  Linux `SO_PEERCRED` then names the peer and the allow-lists apply. Outside
  Linux the standard library exposes no peer credential and `getpeereid` needs
  `x/sys`, which the SDK bans — `Peer.Verified` says which world a caller is in.
- **The gate is the whole PATH, not its last component.** `checkEntry`'s
  `Lstat` sees the socket directory's own entry only, and every lookup — the
  `Mkdir`, the `bind`, the `connect` — follows a link planted at a parent.
  Measured before `chain_unix.go` existed, with `pub` `0777|sticky` (what
  `/tmp` is) and `pub/app` a link: `NewListener` on `pub/app/run/d.sock`
  bound its socket at the link's target, `Dial` accepted the same path, and
  once the planter (who owns its link, so the sticky bit lets it replace it)
  re-pointed `app` at a tree where another listener waited, the client's first
  line reached that listener. Across accounts the planter also owns the
  directory the socket's directory was created in: on macOS an inheritable ACL
  entry it sets there is inherited by the `0700` directory and the `0600`
  socket (observed with `ls -le`), which no mode check sees — and outside
  Linux the peer is not verified.
- **The rule judges each component by the directory holding it, and only
  where ANYBODY can write that directory** (world-writable): an indirection is
  refused (`kind=indirection` — `internal/service/app/lock`'s rule, ADR 0083,
  sticky exempts nothing because planting is a creation), a component owned by
  neither this account nor root is refused (`kind=foreign` — whoever created
  it decides everything below it), and without the sticky bit any component is
  refused (`kind=replaceable`), the socket directory's own entry included
  (`checkHolder`). `/tmp -> private/tmp`, `/var -> private/var` (macOS) and
  `/var/run -> /run` (Linux) sit in directories only root writes and are never
  judged; `TestTheOperatingSystemsOwnLinksAreNotRefused` runs a socket through
  each where the suite can write. A GROUP-writable holder is a deliberate
  arrangement and is not judged, as lock accepts it. Refusals are
  `PATH_UNSAFE`, not `DIRECTORY_UNSAFE`: the remedy is a human looking at a
  component that may be an attack, not a `chmod`.
- **Stricter than lock, on purpose.** lock judges only indirections because a
  lock is shared between accounts by design; this package's directory must be
  this account's, so a foreign or replaceable component above it is the same
  breach one level up. The owner comes from `pathchain.StepValue.Info`, read
  relative to the directory handle the walk holds, never by a second lookup.
- **Audited before and after the `Mkdir`.** Before, so nothing is created
  inside a steered tree (`os.Mkdir` follows a parent link); after, because a
  parent that did not exist at the first audit may have been created by
  somebody else in between.
- **What it does not see, stated.** An accepted component can be replaced only
  by this account, by root, by the owner of the directory holding it or by
  that directory's group — accounts the deployment chose by placing the socket
  there. A socket
  under a tree another account owns therefore trusts that account, and nothing
  here refuses it. ACL entries are not read on any platform (no stdlib API on
  macOS without `x/sys`); a holder this account or root owns is trusted not to
  carry a hostile inheritable entry.
- **Group search is allowed, group write is not.** A deployment that admits an
  on-call group gives the directory `0750` and its group; nobody but the owner
  can plant an entry. This package never widens a directory.
- **Dial, then remove.** A socket at the path is dialled first: one that
  answers belongs to a live process and is refused (`IN_USE`), one that does
  not is a leftover and is removed — only when it is a socket this account
  owns. A regular file there is never touched.

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
  carries a DACL this package BUILDS (`D:P(A;;GA;;;<SID>)`, ADR 0148 §3), so the
  kernel's reader, `internal/kernel/fs/winacl`, is not this package's.

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
