// Package ipc — the components ABOVE the socket's directory, and who could
// steer them: ADR 0083's rule, applied where the directory is the whole of the
// access control (ADR 0148).
//
// # One Lstat sees one component; every lookup crosses all of them
//
// [checkEntry] judges the socket's directory by its own entry: not a link, a
// directory, this account's, written by nobody else. It cannot see a PARENT,
// and every lookup this package makes — the Mkdir that creates the directory,
// the bind that creates the socket, the connect that reaches it — follows a
// link planted at one. Measured on the code before this file, with a link
// planted in a 0777|sticky directory, which is what /tmp is:
//
//	Config.Path     /tmp/ipc…/pub/app/run/d.sock    pub dtrwxrwxrwx, app -> …/elsewhere
//	NewListener     accepted: the socket was bound at …/elsewhere/run/d.sock
//	Dial            accepted
//	app re-planted  -> …/trap, where another listener waited
//	Dial            accepted: the trap's listener read the client's first line
//
// The planter owns its link, so the sticky bit lets it replace that link at
// will: it decides where the directory is created, and later which socket a
// client reaches. Across accounts it also owns the directory the socket's
// directory was created in, and on macOS an inheritable access-control entry
// it puts there is inherited by the 0700 directory Listen creates and by the
// 0600 socket bound inside — observed with ls -le — which no mode check sees.
// Outside Linux the peer is not verified, so that entry is a way in.
//
// # The rule: who could have created a component, and who can replace it
//
// A link at a parent is not evidence on its own — /tmp -> private/tmp and
// /var -> private/var on macOS, /var/run -> /run on most Linux distributions —
// and what separates those from an attack is the directory the component
// lives in, exactly as internal/service/app/lock reasons (ADR 0083). So every
// component above the socket's directory is judged by the directory holding
// it, and only when ANYBODY can write that directory:
//
//   - an indirection is refused ([kindIndirection]): anybody could have
//     planted it. The sticky bit does not exempt it — sticky governs UNLINKING
//     an entry that exists, and planting creates one at a name nobody had
//     taken. This is lock's rule, unchanged.
//   - a component owned by neither this process's user nor root is refused
//     ([kindForeign]): anybody could have created it, and whoever did decides
//     everything below it, down to the entries a directory made there
//     inherits. lock has no such rule because a lock is shared between
//     accounts on purpose; a private socket's directory must be this
//     account's, and its gate is that nobody else reaches it.
//   - without the sticky bit, any component is refused ([kindReplaceable]):
//     anybody can rename it away and put their own in its place. The socket's
//     directory itself is held to the same rule by [checkHolder].
//
// A directory writable by its GROUP is not judged: a directory shared with a
// group is a deliberate arrangement, as lock accepts it.
//
// # What it does not see
//
// A component this rule accepts can be replaced only by this account, by
// root, by the owner of the directory holding it or by that directory's group
// — accounts the deployment chose by placing the socket there. A socket under a tree another
// account owns therefore trusts that account, by the deployment's own choice,
// and nothing here refuses it. The audit runs at Listen — before the directory
// is created and again after — and at every Dial; on Linux the peer's
// credentials are the gate that holds between them.
//
// Package ipc — the socket directory on Unix: created 0700, refused when
// another account could write to it, when it is a link, or when another
// account could steer the path to it (chain_unix.go).
//
// Package ipc is a private socket between processes of one machine: a
// listener only its own account — and the accounts it names — can reach, a
// client that refuses to talk to a socket another account planted, and the
// kernel's word on who is at the other end (ADR 0148).
//
// Two gates, and which one holds where is said rather than assumed:
//
//   - THE DIRECTORY, everywhere. The socket lives in a directory this
//     process's account owns and nobody else may write to; connecting to a
//     Unix socket needs search permission on every directory above it, so a
//     0700 directory admits its owner and root and nobody else. It is checked
//     at Listen and at Dial, never widened by this package — and so is the
//     PATH to it: a component above it that anybody could have planted or
//     created, or can replace, is refused (PATH_UNSAFE, chain_unix.go), since
//     every lookup follows a link planted at a parent.
//   - THE PEER'S CREDENTIALS, where the kernel gives them: SO_PEERCRED on
//     Linux. There the listener refuses a peer whose UID is neither its own
//     nor allowed, whatever the directory says. Elsewhere Peer.Verified is
//     false and the directory is the only gate — stated, not papered over.
//
// Windows has no search permission to gate a socket by its directory: there
// the endpoint is a named pipe (pipe_windows.go) named after Config.Path,
// whose own DACL grants this process's account only, which refuses remote
// clients (PIPE_REJECT_REMOTE_CLIENTS) and which nobody can join once it
// exists (FILE_FLAG_FIRST_PIPE_INSTANCE); each end reads the other's account
// from its process token (Peer.SID), and a client refuses a listener of
// another account before it sends a byte.
//
// The contract is internal/core/proc/ipc (ADR 0160): the peer and the
// connection that carries it, the Listener and Dialer ports this package's
// Listener and Dialer implement, and the codes every refusal carries. This
// package is the engine — and Config, the one configuration both ends share,
// is the engine's (ADR 0074).
//
// Package ipc — the compile-time proof that the engines satisfy the core
// ports (ADR 0160): a method renamed or retyped on either engine fails the
// build here, not in a caller that holds the port.
//
// Package ipc — the peer's credentials on Linux: SO_PEERCRED, what the peer
// had when it connected or listened.
//
// Package ipc — no peer credentials outside Linux without x/sys: the peer is
// unverified and the directory is the gate.
//
// Package ipc — the private endpoint on Windows: a named pipe.
//
// A Unix socket there is a file in a directory, but Windows has no search
// permission to make a directory the gate, so the endpoint is a named pipe,
// the object Windows secures by itself:
//
//   - its DACL grants this process's account only, protected from
//     inheritance (SDDL "D:P(A;;GA;;;<SID>)"), so no other account can open
//     it — Everyone, Authenticated Users and Administrators included;
//   - PIPE_REJECT_REMOTE_CLIENTS refuses a client of another machine;
//   - the first instance is created with FILE_FLAG_FIRST_PIPE_INSTANCE: when
//     the name exists — another daemon, or a squatter that made it first —
//     the listen fails (IN_USE) rather than joining an instance somebody
//     else controls, and the next instance is created before a connection is
//     handed out, so the name never lapses while the listener lives;
//   - each end reads the other's account from its process token
//     (GetNamedPipeClientProcessId / GetNamedPipeServerProcessId, then
//     OpenProcessToken): a client refuses a server of another account before
//     it sends a byte (ENDPOINT_FOREIGN), and opens the pipe at
//     SECURITY_IDENTIFICATION, so the server can learn who it is but never
//     act as it.
//
// The pipe is named after Config.Path, so one configuration serves every
// platform: \\.\pipe\ipc-<16 hex digits of the path's SHA-256>-<base name>.
// Nothing is created in the path's directory.
//
// The handles are opened overlapped and handed to os.NewFile, which puts
// them on the runtime's poller: reads, writes and deadlines are the os
// package's. kernel32 and advapi32 are bound with syscall.NewLazyDLL, as the
// lock domain binds LockFileEx and GetNamedSecurityInfoW — golang.org/x/sys
// is banned SDK-wide (ADR 0018).
//
// Package ipc — the Unix socket: a 0700 directory, a 0600 socket, a leftover
// removed only when nobody answers on it and it is ours, and the peer the
// kernel names where it does.
package ipc
