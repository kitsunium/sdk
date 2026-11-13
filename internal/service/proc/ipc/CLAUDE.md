<!-- updated: 2026-11-13T00:00:00Z -->
# internal/service/proc/ipc/

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

## Why-this-shape

- **A core of its own, with two ports** (ADR 0160 §1). This was "one engine,
  no port" until the reorganisation, and its callers — the framework's
  listeners, the telemetry exporter — held the engine itself, so testing the
  code around them needed a socket on disk. `coreipc.Listener` and
  `coreipc.Dialer` are what a caller holds now; `NewListener` and `NewDialer`
  return the engines behind them, and a test hands the same code `net.Pipe`
  doubles. `Config` stays here: it is the one engine's configuration
  (ADR 0074), and the ports take none.
- **The directory is the gate everywhere; the peer's credentials are a second
  one where they exist.** Connecting to a Unix socket needs search permission
  on its directory, so a 0700 directory already keeps other accounts out; where
  the kernel names the peer the allow-lists then apply: `SO_PEERCRED` on Linux,
  and since #1093 `getsockopt(SOL_LOCAL, LOCAL_PEERCRED)` (`struct xucred`) on
  FreeBSD and DragonFly and `LOCAL_PEEREID` (`struct unpcbid`) on NetBSD,
  through `syscall.Syscall6(SYS_GETSOCKOPT, …)` and a struct decoded from a
  byte buffer (`peer_bsd.go`, `peer_xucred.go`, `peer_unpcbid.go`; the
  constants cited from `sys/un.h`). On those platforms (`kernelNamesPeer`,
  `peer_named.go`) a peer the kernel did NOT name — a failed getsockopt — is
  refused, `PEER_REFUSED` with `reason` "kernel did not name the peer", never
  let through (ADR 0031). darwin (raw syscalls deprecated), OpenBSD (pinned
  syscalls), illumos and Solaris (`getpeerucred` is libc's) stay unverified
  and the directory is the gate there (`peer_other.go`, `peer_unnamed.go`) —
  `Peer.Verified` says which world a caller is in. Windows has its own rule
  since #1349: a client whose token cannot be read is refused, as on Linux —
  it used to be admitted as the DACL's, when the DACL named this account
  alone. The BSD
  readers never ran on this lot's VM: `peer_bsd_internal_test.go` runs on the
  e2e-cross FreeBSD and NetBSD lanes.
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
- **The socket is opened to the lists, no wider (#1092, ADR 0148 amendment
  2026-11-11).** `connect(2)` needs write permission on the socket, so a
  `0600` socket admitted nobody the lists named but root. `socketPosture`:
  `0600` when they name nobody but this account and root; `0660` and the one
  group they name when they name no other user — the kernel's own check then
  admits that group's members, supplementary ones included, and nobody else,
  even where no peer is named —; `0666` for another user or several groups. A
  group the socket cannot take (this account not in it) is `LISTEN_FAILED`.
  The group and mode are set between `bind(2)` and `listen(2)`
  (`bind_unix.go`): a connect to a socket that does not listen is refused, so
  the umask's mode never admits anyone, and the process's umask is never
  touched. Where the kernel names no peer, a `0666` socket under a directory
  anybody may search is `DIRECTORY_UNSAFE` (`checkReach`): nothing would gate
  it.
- **A supplementary group admits, read from the kernel (#1092).** `admit`
  takes the peer's groups beside its `PeerValue`: `SO_PEERGROUPS` on Linux —
  the group list of the credentials `SO_PEERCRED` reads, recorded at connect,
  so no pid is read again and no `/proc` file can name another process —, the
  `xucred`'s sixteen on FreeBSD and DragonFly, none on NetBSD (`unpcbid` has
  the egid alone). They are read only when `AllowGIDs` is set, and a kernel
  that does not answer leaves the effective group alone: fewer admitted, never
  more. `PeerValue` does not carry them — a slice would make it
  incomparable — so a caller still sees the effective group only.
- **Files pass to a named peer only (#936).** On Unix `Conn.Conn` is a
  `fileConn` (`files_unix.go`) embedding the `*net.UnixConn`, offering the core
  siblings `FileSender` and `FileReceiver` (ADR 0039): `SendFiles` writes
  `SCM_RIGHTS` with `WriteMsgUnix`, refused (`FD_PASS_REFUSED`, 0.3.91.10) to
  a peer the kernel did not name — a descriptor never reaches an account
  nobody vouched for — and for an empty payload; `RecvFiles` sizes the control
  buffer for `max` (refused at zero), so the kernel closes the excess
  (`MSG_CTRUNC`, reported as `FD_PASS_REFUSED` "truncated" beside the files
  that fit) and none leaks into the process; the net package receives them
  close-on-exec. `TestPassFiles` (Linux) pins a file, the 64 against `max=4`
  with the descriptor count unchanged, and the unnamed peer;
  `TestHandOverAListener` (`files_handover_internal_test.go`,
  every Unix and Windows) passes a bound TCP listener to a receiving process —
  the test binary run again in the `SDK_IPC_HANDOVER_RECEIVER` role, taken out
  of the inherited environment before it is set, starting nothing and dead
  within 30 s — closes the sender's two descriptors, and has the receiver
  accept a connection on it end to end — a ping and its pong — where the
  kernel names the peer; elsewhere (darwin, OpenBSD, illumos, Solaris) it
  asserts the refusal and skips. **Windows (`files_windows.go`)**: a pipe carries bytes, so
  the files travel in band, in a frame — `SDK2`, the count, the payload's
  length, the entries, each tagged a handle (its value) or a socket (its
  `WSAPROTOCOL_INFOW`), the payload — and the receiver must call
  `RecvFiles` where the sender called `SendFiles` (anything else is refused,
  the stream then out of step). The sender duplicates each file into its own
  table (private, not inheritable); the receiver duplicates each value OUT of
  the sender's process with `DUPLICATE_CLOSE_SOURCE` and closes the excess
  there the same way. That is the issue's `DuplicateHandle` turned round, on
  purpose: pushed into the receiver, the values would be numbers the receiver
  is told and wraps, and a peer that cannot open the receiver could name a
  handle the receiver already holds; pulled, the receiver only ever gets
  handles it made from what the sender holds. The handle on the peer's
  process (`PROCESS_DUP_HANDLE`) is opened when the connection completes and
  kept until `Close`, and a process created after the connection is refused,
  so the pid cannot be another process's by then; without it (an unnamed
  peer, a peer this account may not open) `RecvFiles` is `FD_PASS_REFUSED`
  and the sender keeps what it sent. A send whose write failed after a byte
  leaves the duplicates to the receiver rather than risk closing a handle
  value this process reused. **A socket** — a TCP listener's `File()`, which Go
  1.27 gives on Windows — is not a handle `DuplicateHandle` may copy: the
  sender tells it by `getsockopt(SO_TYPE)`, prepares it for the peer's process
  with `WSADuplicateSocketW` (the pid pinned by the process handle held since
  the connection, refused without it) and sends the `WSAPROTOCOL_INFOW`; the
  receiver makes its own with `WSASocketW(FROM_PROTOCOL_INFO)`, overlapped and
  not inheritable, as the net package dups a socket within one process — both
  bound from `ws2_32.dll` with `syscall.NewLazyDLL`, no `x/sys`. A socket past
  `max` is made and closed here; one prepared for a frame that was never
  written stays the peer's until it exits. Only `SDK2` is written; the first
  layout, `SDKF` (v0.60.0, #1316: bare handle values), is still read so two
  versions pass files during an upgrade (`TestAFirstLayoutFrameIsStillRead`;
  ADR 0148's version rule: write the newest, read the one before for at least
  one release), and any other magic or entry kind is `FD_PASS_REFUSED`
  (`TestAFrameOfAnUnknownKindIsRefused`). None of the Windows
  half has run here (e2e-cross's Windows lane runs it).
- **A pipe name that exists is judged by its holder (#1318).** Named pipes
  share one machine-wide namespace, and the name used to be derived from the
  path alone, so another account could make it first and the listener read
  that as `IN_USE` for as long as the squatter held it. The name now carries
  the account's SID (`\\.\pipe\ipc-<SID>-<hash>-<base>`), so two accounts
  with one path never meet, and the pipe is created OWNED by that SID
  (`O:<SID>`) — an elevated token's default owner is Administrators, which
  would make our own second listener look foreign. When the first-instance
  create fails, `holder` creates one more instance (no
  `FILE_FLAG_FIRST_PIPE_INSTANCE`), reads the owner with `GetSecurityInfo`
  (the duplex server end holds `READ_CONTROL`) and closes it at once: this
  account is `IN_USE`; another owner, or a pipe that admits no instance of
  ours (every pipe this package makes grants its account `GA`), is
  `ENDPOINT_SQUATTED` (`0.3.91.11`, exit 78), `owner` or `cause` field. What
  it does NOT do: prevent the squat — a SID is no secret, the name stays
  computable; it is now named instead of mistaken for our own daemon. Two
  windows are accepted: the probe instance listens for the moment it lives (a
  client of ours reaching it sees a broken pipe and dials again), and a pipe
  gone between the two creates is made again by the probe and read as ours
  (`IN_USE`, cleared by a retry). Tests: `pipe_windows_internal_test.go` (a
  pipe pre-created under `D:P(A;;GR;;;WD)` and one owned by another account
  through `listenAs`, the SID in the name, the owner read back) and
  `TestASecondPipeListenerIsInUse`; they run on e2e-cross's Windows lane only.
- **A Windows pipe opened to listed SIDs (#1349, ADR 0148 amendment
  2026-11-13).** `Config.AllowSIDs` holds SIDs as text; `canonicalSIDs`
  converts each with `ConvertStringSidToSid` and back, so the DACL and the
  admission compare one spelling and nothing an entry spells reaches the
  descriptor's text; an entry that is not a SID, and Everyone, Anonymous
  Logon or the Null SID, are `MISCONFIGURED` with `rule` and `sid` — at
  `NewListener`, `NewDialer` and `Dial` — and a non-empty list is
  `MISCONFIGURED` on Unix (`socket.go`'s `checkSIDs`). `AllowUIDs` and
  `AllowGIDs` stay what they were on Windows: checked for a negative id, then
  ignored, as `Config`'s doc and ADR 0148 point 6 say — not refused, so one
  configuration still serves every platform. **The DACL is the first
  barrier**: each listed SID gets `clientAccess` (`0x0012008B`,
  `FILE_GENERIC_READ` and `FILE_WRITE_DATA`), not the issue's
  `GENERIC_READ|GENERIC_WRITE`: `FILE_GENERIC_WRITE` carries
  `FILE_APPEND_DATA`, which on a pipe IS `FILE_CREATE_PIPE_INSTANCE`
  (Microsoft's "Named Pipe Security and Access Rights" says so and advises the
  individual rights), so a listed account could have served an instance of the
  pipe and posed as the listener to its clients. `Dial` opens with the same
  mask, for every account. The first instance keeps
  `FILE_FLAG_FIRST_PIPE_INSTANCE` and the owner `O:<SID>`. **The token is read
  again** at every `Accept` (`clientIdentity`): through the pipe, with
  `ImpersonateNamedPipeClient` then `OpenThreadToken(TOKEN_QUERY, OpenAsSelf)`
  then `RevertToSelf`, on a goroutine locked to its OS thread — never by the
  client's pid, which may since name another process, and never with this
  thread handed back to the runtime while it acts as the client: a failed
  `RevertToSelf` leaves the goroutine locked, and the runtime ends the thread
  with it. `admittedBy` mirrors the kernel's check of the DACL: the user
  (unless a restricted token made it deny-only) or an enabled group is the
  account or listed, and a restricted token's restricting SIDs must name one
  too. A token that cannot be read — `SECURITY_ANONYMOUS`, a failed
  impersonation — is `PEER_REFUSED` and counted. **Dial** tries this
  account's pipe, then each listed SID's in the list's order; only a pipe
  that does not exist passes the turn, so a refused name is never stepped
  over. This account's pipe keeps its rule (the server's process token, read
  or `DIAL_FAILED`); a listed account's pipe must be owned by that account
  and, where the server's process token can be read, run by it
  (`ENDPOINT_FOREIGN` with `owner` or `sid`); when it cannot — another
  account's process, unless this one is privileged — the owner alone names
  it, `Peer.Verified` false, which also keeps files from being passed to it.
  The owner is sound there because only it can serve an instance of a pipe
  this package made. **Not verified anywhere**: a second account. Every
  Windows row runs as the CI runner's one account (`sids_windows_internal_test.go`,
  `TestAPipeOpenedToAListAdmitsThisAccount`): the DACL read back from the
  kernel, the canonical list, the refusals at every entry point, the re-check
  with the listener standing as LocalSystem, an anonymous client, restricted
  tokens made with `CreateRestrictedToken`, and the dial order with squatted
  names (the row a pipe Administrators own needs an elevated token and skips,
  saying so, elsewhere). Whether a non-privileged listener may open another
  account's identification token (`OpenAsSelf`, the token's own DACL) has not
  run: where it may not, that client is refused — never admitted.
- **The directory rule is split by side (#1092).** `NewListener` keeps
  `checkDir`: the directory is this account's. `Dial` runs `checkDialDir`: the
  same chain, holder and entry rules — a real directory nobody but its owner can
  write — but NO owner rule, because the directory is the listener's, usually
  another account's; refusing it made the documented on-call deployment
  undiallable by the very group it admits, and root too. Who may be dialled is
  then the socket FILE's owner: this account, an `AllowUIDs` member or an
  `AllowGIDs` group, `ENDPOINT_FOREIGN` with `uid` and `gid` otherwise. A client
  outside the group is refused by the kernel at the 0750 directory
  (`DIAL_FAILED`). `dialWith` takes `lstat` and the effective uid so
  `TestDialAnotherAccountsSocket` stands as euid 1001 before user 1000's
  daemon. The row with two real accounts is
  `TestAnAllowedAccountExchangesALineAndAStrangerIsRefused`
  (`account_linux_external_test.go`, root only — the linux-init lane): copies
  of the test binary run as uids 61000 (the daemon), 61001 (in group 61100,
  supplementary) and 61002, guarded by `TestMain` (a helper starts nothing,
  and a process a helper started plays no role and runs no test). A user
  namespace mapping one uid cannot stand in: the kernel's uid stays the
  test's, so `SO_PEERCRED` and the socket's mode would see one account.
- **Dial, then remove.** A socket at the path is dialled first: one that
  answers belongs to a live process and is refused (`IN_USE`), one that does
  not is a leftover and is removed — only when it is a socket this account
  owns. A regular file there is never touched.
- **Only "refused" means dead (#452).** `nobodyListens` admits two dial
  failures as a leftover: `ECONNREFUSED` and `ENOENT` (the file gone between
  the `Lstat` and the dial). Every other failure is `IN_USE`, its cause in a
  field: a live owner whose accept queue is full answers `EAGAIN` on Linux,
  and a busy one may let the one-second dial expire — reading either as dead
  removed a running server's socket and bound over it, and its clients
  reached the impostor. The BSDs and macOS answer a full backlog with
  `ECONNREFUSED` itself, which a dead socket gives too, so there a busy owner
  still looks dead; `TestABusyLiveSocketIsNeverTakenOver` runs on Linux and
  says so elsewhere.
- **`$RUNTIME_DIRECTORY` is a list (#455).** Several `RuntimeDirectory=` make
  systemd join their paths with `:` (systemd.exec(5)); `RuntimeDirOr` takes
  the first entry, and an empty or relative first entry passes the chain on.

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
