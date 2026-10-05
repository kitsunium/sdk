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
//     (and root) get through. On Windows the endpoint is a named pipe named
//     after the path, whose own DACL grants this account only, which refuses
//     remote clients and which nobody can join once it exists.
//   - The PEER'S CREDENTIALS, where the kernel gives them: on Linux,
//     SO_PEERCRED names the peer's user, group and process, and a peer that is
//     neither this account nor in Config.AllowUIDs / AllowGIDs is closed
//     before Accept returns it. On Windows each end reads the other's account
//     from its process token ([Peer].SID) and refuses another one. Elsewhere
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
// alone and refused ([CodeInUse]); one that does not — what a killed daemon
// leaves — is removed and replaced, provided it IS a socket this account
// owns. A client refuses a socket file another account owns
// ([CodeEndpointForeign]) before it sends a byte: a request never reaches an
// impostor.
//
// # Where
//
// [RuntimeDir] names an application's directory: $RUNTIME_DIRECTORY (systemd),
// $XDG_RUNTIME_DIR/<app>, %LOCALAPPDATA%\<app> on Windows, else
// <tmp>/<app>-<uid>. A path is at most the shortest sun_path any supported
// kernel has, less its NUL.
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
