//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/ipc .

// Package ipc is a private socket between processes of one machine — a
// daemon and the short-lived clients that talk to it, a product and the tool
// that attaches to it (ADR 0144).
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
package ipc

import (
	"context"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcipc "github.com/kitsunium/sdk/internal/service/ipc"
)

// The codes, for errs.HasCode: a caller must tell "another daemon runs"
// (IN_USE: talk to it) from "the directory is not private" (fix the
// deployment) from "nobody answers" (start one).
const (
	CodeMisconfigured   errs.Code = svcipc.CodeMisconfigured
	CodeDirectoryUnsafe errs.Code = svcipc.CodeDirectoryUnsafe
	CodeInUse           errs.Code = svcipc.CodeInUse
	CodeListenFailed    errs.Code = svcipc.CodeListenFailed
	CodePeerRefused     errs.Code = svcipc.CodePeerRefused
	CodeDialFailed      errs.Code = svcipc.CodeDialFailed
	CodeEndpointForeign errs.Code = svcipc.CodeEndpointForeign
	CodeClosed          errs.Code = svcipc.CodeClosed
)

type (
	// Config is where a private socket lives and who, besides its own
	// account, may use it.
	Config = svcipc.Config
	// Peer is who is at the other end of a connection, as the kernel says.
	Peer = svcipc.PeerValue
	// Conn is a connection with its peer's identity.
	Conn = svcipc.Conn
	// Listener accepts the connections of admitted peers only.
	Listener = svcipc.Listener
)

// Listen opens the private socket at cfg.Path.
func Listen(cfg Config) (*Listener, error) { return svcipc.NewListener(&cfg) }

// Dial connects to the private socket at cfg.Path, within ctx and one second.
func Dial(ctx context.Context, cfg Config) (*Conn, error) { return svcipc.Dial(ctx, &cfg) }

// RuntimeDir is where an application's private sockets belong on this
// machine. It creates nothing.
func RuntimeDir(app string) string { return svcipc.RuntimeDir(app) }
