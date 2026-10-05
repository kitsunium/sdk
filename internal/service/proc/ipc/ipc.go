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

// addr is Listener.Addr's body: decl_gen.go writes Listener.Addr, from the
// design, as one call of it.
func (l *Listener) addr() net.Addr { return l.ln.Addr() }

// path is Listener.Path's body: decl_gen.go writes Listener.Path, from the
// design, as one call of it.
func (l *Listener) path() string { return l.cfg.Path }

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
