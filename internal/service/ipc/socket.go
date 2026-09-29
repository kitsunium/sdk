//go:build !windows

// Package ipc — the Unix socket: a 0700 directory, a 0600 socket, a leftover
// removed only when nobody answers on it and it is ours, and the peer the
// kernel names where it does.
package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// socketAcceptor accepts on a Unix socket; its peers are named by peerOf.
type socketAcceptor struct {
	*net.UnixListener
}

// accept returns the next connection and its peer as the kernel names it.
func (s socketAcceptor) accept() (net.Conn, PeerValue, error) {
	c, err := s.AcceptUnix()
	if err != nil {
		return nil, PeerValue{}, err
	}
	return c, peerOf(c), nil
}

// listen opens the Unix socket at cfg.Path, once its directory is private
// and what was at the path is known to be a leftover.
func listen(cfg *Config) (*Listener, error) {
	if err := prepareDir(filepath.Dir(cfg.Path)); err != nil {
		return nil, err
	}
	if err := clearStale(cfg); err != nil {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: cfg.Path, Net: "unix"})
	if err != nil {
		return nil, errs.Wrap(ListenFailed, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("cause", err.Error()))
	}
	// The listener removes the file it created when it closes, and only then.
	ln.SetUnlinkOnClose(true)
	if err := os.Chmod(cfg.Path, 0o600); err != nil {
		closeBestEffort(ln, cfg.Path)
		return nil, errs.Wrap(ListenFailed, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("cause", err.Error()))
	}
	return &Listener{cfg: *cfg, ln: socketAcceptor{ln}, self: os.Geteuid()}, nil
}

// clearStale removes a socket nobody answers on, and refuses one somebody
// does. What is not a socket, or not ours, is never removed.
func clearStale(cfg *Config) error {
	info, err := os.Lstat(cfg.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return errs.Wrap(ListenFailed, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("cause", err.Error()))
	case info.Mode()&os.ModeSocket == 0:
		return errs.Wrap(DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "the socket path holds something that is not a socket"),
			errs.String("path", cfg.Path))
	}
	if uid, known := ownerOf(info); known && uid != os.Geteuid() {
		return errs.Wrap(EndpointForeign, errs.WrapParams{}, errs.String("path", cfg.Path), errs.Int("uid", uid))
	}
	c, err := net.DialTimeout("unix", cfg.Path, dialTimeout)
	if err == nil {
		closeBestEffort(c, cfg.Path)
		return errs.Wrap(InUse, errs.WrapParams{}, errs.String("path", cfg.Path))
	}
	if err := os.Remove(cfg.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errs.Wrap(ListenFailed, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("cause", err.Error()))
	}
	return nil
}

// admits is the Unix admission: this account, or an allowed user or group,
// or a peer the kernel did not name — the directory admitted it.
func (l *Listener) admits(p PeerValue) error { return admit(p, l.self, &l.cfg) }

// dial connects to the Unix socket at cfg.Path, once its directory and its
// file are known to be this account's (or an allowed one's). A directory
// that does not exist is nobody listening — the first client of a daemon not
// started yet —, DIAL_FAILED, not an unsafe directory.
func dial(ctx context.Context, cfg *Config) (*Conn, error) {
	dir := filepath.Dir(cfg.Path)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, errs.Wrap(DialFailed, errs.WrapParams{}, errs.String("path", cfg.Path),
			errs.String("cause", "nobody listens: the directory does not exist"))
	}
	if err := checkDir(dir); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(cfg.Path); err == nil {
		if uid, known := ownerOf(info); known && uid != os.Geteuid() && !slices.Contains(cfg.AllowUIDs, uid) {
			return nil, errs.Wrap(EndpointForeign, errs.WrapParams{}, errs.String("path", cfg.Path), errs.Int("uid", uid))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", cfg.Path)
	if err != nil {
		return nil, errs.Wrap(DialFailed, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("cause", err.Error()))
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		closeBestEffort(c, cfg.Path)
		return nil, errs.Wrap(DialFailed, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("cause", "not a unix connection"))
	}
	p := peerOf(uc)
	if err := admit(p, os.Geteuid(), cfg); err != nil {
		closeBestEffort(c, cfg.Path)
		return nil, err
	}
	return &Conn{Conn: c, Peer: p}, nil
}
