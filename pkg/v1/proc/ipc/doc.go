// Package ipc is a private socket between processes of one machine — a
// daemon and the short-lived clients that talk to it, a product and the tool
// that attaches to it (ADR 0148).
//
//	cfg := ipc.Config{Path: filepath.Join(ipc.RuntimeDir("statusline"), "daemon.sock")}
//	ln, err := ipc.Listen(cfg)          // the daemon
//	c, err := ipc.Dial(ctx, cfg)        // a client; c.Peer says who listens
//
// # Who can connect
//
// Two gates, and the package says which one holds where:
//
//   - The DIRECTORY, everywhere. The socket lives in a directory its account
//     owns and nobody else may write to — created 0700 when missing, refused
//     otherwise ([CodeDirectoryUnsafe]) — and the socket file is 0600.
//     Connecting needs search permission on the directory, so only its owner
//     (and root) get through. The accounts Config.AllowUIDs / AllowGIDs name
//     get the socket opened to them — 0660 and the group for one group,
//     0666 for another user or several groups —, the directory, which the
//     deployment opens to them, staying the first gate. On Windows the endpoint is a named pipe named
//     after this account and the path, whose own DACL grants this account
//     and the SIDs Config.AllowSIDs lists — the right to read and write it,
//     never to serve an instance of it —, which refuses remote clients and
//     which nobody can join once it exists; a name another account took
//     first is refused as squatted ([CodeEndpointSquatted]), never mistaken
//     for a daemon of ours.
//   - The PEER'S CREDENTIALS, where the kernel gives them: on Linux,
//     SO_PEERCRED names the peer's user, group and process, and a peer that is
//     neither this account nor in Config.AllowUIDs / AllowGIDs — by its
//     user, its effective group or a supplementary group the kernel
//     recorded — is closed before Accept returns it — and so, since #1093,
//     do FreeBSD, DragonFly and NetBSD (the effective group alone there),
//     where an unnamed peer is refused too. On Windows the listener reads
//     each client's token again ([Peer].SID) and refuses one whose user and
//     enabled groups name neither this account nor a listed SID, or whose
//     token it cannot read; a client refuses a listener of another account
//     than the one whose pipe it opened — its own, or a listed one's when
//     its own does not exist. On darwin, OpenBSD, illumos and Solaris
//     [Peer].Verified is false and the directory is the only gate.
//
// # The path to the directory is part of the gate
//
// Every lookup — the directory's creation, the bind, the connect — follows a
// link planted at a PARENT of the directory, so on Unix Listen and Dial walk
// the whole path one component at a time and judge each by the directory
// holding it, where ANYBODY can write that directory: a link there could have
// been planted by anyone, a component another account owns could have been
// created by anyone, and without the sticky bit anyone can replace what is
// there. Each is refused ([CodePathUnsafe]) before anything is created or a
// byte is sent. The links an operating system ships — /tmp and /var on macOS,
// /var/run on Linux — live in directories only root writes and are accepted.
// A socket placed under a tree another account owns trusts that account:
// [RuntimeDir] places it under this account's own.
//
// # Nothing is taken over
//
// Listen dials a socket already at the path first. One that answers is left
// alone and refused ([CodeInUse]), and so is one whose owner is alive but
// busy — its accept queue full, or the dial out of time; only one the kernel
// says nobody listens on — what a killed daemon leaves — is removed and
// replaced, provided it IS a socket this account
// owns. A client refuses a socket file another account owns
// ([CodeEndpointForeign]) before it sends a byte: a request never reaches an
// impostor.
//
// # Where
//
// [RuntimeDir] names an application's directory: $RUNTIME_DIRECTORY
// (systemd's, its first entry when it lists several), $XDG_RUNTIME_DIR/<app>, %LOCALAPPDATA%\<app> on Windows, else
// <tmp>/<app>-<uid>. [RuntimeDirOr] ends the same chain on a directory the
// caller names instead of <tmp> (ADR 0182). A path is at most the shortest
// sun_path any supported kernel has, less its NUL.
//
// # Ports, and the doubles they admit
//
// [Listener] and [Dialer] are interfaces, as net.Listener is: [Listen] and
// [NewDialer] return the socket engine behind them, and code that holds one
// can be handed a double in a test — connections from net.Pipe, with the
// [Peer] the test chooses — instead of a socket on disk. Both are frozen at
// the methods they have; a capability added later is a second interface an
// endpoint may also implement, never a method added to these.
package ipc
