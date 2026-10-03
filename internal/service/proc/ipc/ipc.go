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
package ipc

import (
	"context"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxPath is the longest socket path every supported kernel accepts: 104
// bytes of sun_path on the BSDs and macOS, 108 on Linux, and one for the NUL.
const maxPath int = 103

// dialTimeout bounds Dial and the liveness probe Listen makes on a socket
// already at the path. A local socket answers in microseconds; a second is a
// process that is not coming.
const dialTimeout time.Duration = time.Second

// Config is where a private socket lives and who, besides its own account,
// may use it. The same value serves the listener and its clients.
type Config struct {
	// Path is the socket's absolute path. Its directory is created 0700 when
	// missing and refused when another account could write to it.
	Path string
	// AllowUIDs and AllowGIDs admit peers besides this process's own user:
	// an on-call group declared at deployment, say. They are enforced where
	// the kernel names the peer (Peer.Verified); the directory must still
	// let those accounts reach the socket, which is the deployment's to
	// arrange — this package never widens a directory. Windows has no UID:
	// there the pipe's DACL admits this account only, and the lists admit
	// nobody more.
	AllowUIDs, AllowGIDs []int
}

// Listener accepts the connections of admitted peers only; a peer the
// kernel names and the configuration does not admit is closed and counted.
// It is the engine behind the core Listener port.
type Listener struct {
	cfg  Config
	ln   acceptor
	self int
	// selfSID is this process's account on Windows, empty elsewhere.
	selfSID string

	mu      sync.Mutex
	closed  bool
	refused int64
}

// acceptor is the endpoint a Listener accepts on: a Unix socket
// (socket.go), a named pipe on Windows (pipe_windows.go). accept returns the
// next connection and what the kernel says of its peer; admission is the
// Listener's.
type acceptor interface {
	accept() (net.Conn, coreipc.PeerValue, error)
	Close() error
	Addr() net.Addr
}

// validate refuses a configuration no endpoint can be built from.
func (c *Config) validate() error {
	switch {
	case c.Path == "" || !filepath.IsAbs(c.Path):
		return errs.Wrap(coreipc.Misconfigured, errs.WrapParams{}, errs.String("rule", "path must be absolute"))
	case len(c.Path) > maxPath:
		return errs.Wrap(coreipc.Misconfigured, errs.WrapParams{}, errs.String("rule", "path longer than sun_path"),
			errs.Int("length", len(c.Path)), errs.Int("max", maxPath))
	case slices.ContainsFunc(c.AllowUIDs, negative), slices.ContainsFunc(c.AllowGIDs, negative):
		return errs.Wrap(coreipc.Misconfigured, errs.WrapParams{}, errs.String("rule", "a UID or GID is negative"))
	}
	return nil
}

// negative reports whether n cannot be a UID or a GID.
func negative(n int) bool { return n < 0 }

// NewListener opens the private socket at cfg.Path. A socket already there is
// dialled first: one that answers is refused (IN_USE) and left alone, one
// that does not is a leftover of a dead process and is removed — provided it
// is a socket this account owns. The socket file is made 0600. On Windows the
// named pipe is created with FILE_FLAG_FIRST_PIPE_INSTANCE: a name that
// exists is IN_USE, whoever made it.
func NewListener(cfg *Config) (*Listener, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return listen(cfg)
}

// Accept returns the next connection of an admitted peer. A peer the kernel
// names and the configuration does not admit is closed at once, counted
// (Refused), and never returned.
func (l *Listener) Accept() (*coreipc.Conn, error) {
	for {
		c, p, err := l.ln.accept()
		if err != nil {
			l.mu.Lock()
			closed := l.closed
			l.mu.Unlock()
			if closed {
				return nil, errs.Wrap(coreipc.Closed, errs.WrapParams{}, errs.String("path", l.cfg.Path))
			}
			return nil, errs.Wrap(coreipc.ListenFailed, errs.WrapParams{}, errs.String("path", l.cfg.Path), errs.String("cause", err.Error()))
		}
		if err := l.admits(p); err != nil {
			closeBestEffort(c, l.cfg.Path)
			l.mu.Lock()
			l.refused++
			l.mu.Unlock()
			continue
		}
		return &coreipc.Conn{Conn: c, Peer: p}, nil
	}
}

// Refused is how many connections Accept closed because their peer was not
// admitted.
func (l *Listener) Refused() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refused
}

// Addr is the listener's address.
func (l *Listener) Addr() net.Addr { return l.ln.Addr() }

// Path is the socket's path.
func (l *Listener) Path() string { return l.cfg.Path }

// Close stops accepting and removes the socket file (a named pipe vanishes
// with its last handle).
func (l *Listener) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return l.ln.Close()
}

// admit says whether p may use a socket of account self under cfg. An
// unverified peer passed the directory, which is the only gate there is.
func admit(p coreipc.PeerValue, self int, cfg *Config) error {
	if !p.Verified || p.UID == self || slices.Contains(cfg.AllowUIDs, p.UID) || slices.Contains(cfg.AllowGIDs, p.GID) {
		return nil
	}
	return errs.Wrap(coreipc.PeerRefused, errs.WrapParams{}, errs.Int("uid", p.UID), errs.Int("gid", p.GID))
}

// Dialer connects to the private socket a configuration names: the engine
// behind the core Dialer port. Code that holds the port dials the same
// endpoint as often as it needs to, and a test hands that code a double.
type Dialer struct {
	cfg Config
}

// NewDialer validates cfg and returns a Dialer for it. The allow-lists are
// copied, so a caller that later edits its slices does not change who this
// dialer admits.
func NewDialer(cfg *Config) (*Dialer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	own := Config{Path: cfg.Path, AllowUIDs: slices.Clone(cfg.AllowUIDs), AllowGIDs: slices.Clone(cfg.AllowGIDs)}
	return &Dialer{cfg: own}, nil
}

// Dial connects to the dialer's socket, exactly as the package-level Dial
// does with the same configuration.
func (d *Dialer) Dial(ctx context.Context) (*coreipc.Conn, error) {
	return dial(ctx, &d.cfg)
}

// Dial connects to the private socket at cfg.Path, within ctx and
// dialTimeout. It refuses first — before a byte is sent — a directory
// another account could write to and a socket file another account owns, so
// a client never hands a request to an impostor; and where the kernel names
// the listener, it refuses one that is neither this account nor allowed. On
// Windows it refuses a pipe whose server runs as another account.
func Dial(ctx context.Context, cfg *Config) (*coreipc.Conn, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return dial(ctx, cfg)
}

// RuntimeDir is where an application's private sockets belong on this
// machine: $RUNTIME_DIRECTORY (systemd's), then $XDG_RUNTIME_DIR/<app>, then
// %LOCALAPPDATA%\<app> on Windows, else <tmp>/<app>-<uid>. It creates
// nothing; Listen does, at 0700.
func RuntimeDir(app string) string {
	if d := os.Getenv("RUNTIME_DIRECTORY"); d != "" && filepath.IsAbs(d) {
		return d
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, app)
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, app)
		}
	}
	return filepath.Join(os.TempDir(), app+"-"+strconv.Itoa(os.Geteuid()))
}

// closeBestEffort closes a connection this package refuses or no longer
// needs. Its error changes nothing — the connection is abandoned either way —
// so it is logged, never returned in place of the verdict that caused it.
func closeBestEffort(c io.Closer, path string) {
	//: a close that fails is worth a line, not a changed outcome.
	if err := c.Close(); err != nil {
		//: the path says which socket, the error what the kernel said.
		log.Printf("ipc: closing a connection on %s: %v", path, err)
	}
}
