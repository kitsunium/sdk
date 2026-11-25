# Notes — <!-- updated: 2026-11-13T00:00:00Z -->

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles. `CLAUDE.md` keeps what to obey; this file holds the rest.

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
