//go:build !windows

package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Socket modes: the narrowest that lets every account the lists name
// connect(2), which needs write permission on the socket and nothing else —
// a read bit grants nothing on a socket, so 0660 and 0666 are written as
// every service manager writes them.
const (
	// ownerOnly admits this account (and root, whom no mode stops).
	ownerOnly os.FileMode = 0o600
	// ownerAndGroup admits the socket's group besides.
	ownerAndGroup os.FileMode = 0o660
	// everyAccount admits every account that reaches the socket; the
	// directory, and the listener's admission, are the gates then.
	everyAccount os.FileMode = 0o666
	// anybodySearches is the directory bit that lets every account through.
	anybodySearches os.FileMode = 0o001
)

// credentials are what the Unix admission reads beside a peer's PeerValue:
// its supplementary groups, as the kernel recorded them, where they are read.
type credentials = []int

// checkSIDs refuses a SID list: Unix has no SID, and a list it cannot honour
// would admit nobody it names while seeming to (#1349). An empty list is the
// only one there is here.
func checkSIDs(sids []string) error {
	if len(sids) == 0 {
		return nil
	}
	return errs.Wrap(coreipc.Misconfigured, errs.WrapParams{}, errs.String("rule", "AllowSIDs is Windows's: a SID names no account here"),
		errs.Int("sids", len(sids)))
}

// socketAcceptor accepts on a Unix socket; its peers are named by peerOf, and
// their supplementary groups read (peerGroups) when an allowed group may
// need them.
type socketAcceptor struct {
	*net.UnixListener
	groups bool
}

// posture is the socket file's mode, the group it is given (-1 keeps the one
// bind gave it), and whether that mode lets every account connect.
type posture struct {
	mode   os.FileMode
	gid    int
	others bool
}

// accept returns the next connection, its peer as the kernel names it and,
// when the acceptor reads them, the peer's groups.
func (s socketAcceptor) accept() (conn net.Conn, peer coreipc.PeerValue, groups []int, err error) {
	c, err := s.AcceptUnix()
	if err != nil {
		return nil, coreipc.PeerValue{}, nil, err
	}
	peer = peerOf(c)
	if s.groups {
		groups = peerGroups(c)
	}
	return withFiles(c, peer), peer, groups, nil
}

// socketPosture is the narrowest posture that lets the accounts cfg names
// connect (#1092, the owner's decision of 2026-11-10): 0600 when it names
// none but this account and root; 0660 and the group when it names a single
// group and no other user — the kernel's own check then admits that group's
// members, supplementary ones included, and nobody else; 0666 when it names
// another user or several groups, which no one group can hold.
func socketPosture(cfg *Config, self int) posture {
	foreignUser := slices.ContainsFunc(cfg.AllowUIDs, func(uid int) bool { return uid != self && uid != 0 })
	groups := slices.Compact(slices.Sorted(slices.Values(cfg.AllowGIDs)))
	switch {
	case foreignUser || len(groups) > 1:
		return posture{mode: everyAccount, gid: -1, others: true}
	case len(groups) == 1:
		return posture{mode: ownerAndGroup, gid: groups[0]}
	}
	return posture{mode: ownerOnly, gid: -1}
}

// checkReach refuses a socket every account may connect to in a directory
// every account may search, where the kernel names no peer (named false):
// nothing would gate it — not the directory, not the mode, not the
// admission, which admits a peer it cannot name. Where the kernel names
// peers, the admission refuses who the lists do not name, and a directory
// whose group is the allowed accounts' still keeps the others out first.
func checkReach(dir string, p posture, named bool) error {
	if !p.others || named {
		return nil
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return errs.Wrap(coreipc.DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "the directory cannot be read"),
			errs.String("path", dir), errs.String("cause", err.Error()))
	}
	if info.Mode().Perm()&anybodySearches != 0 {
		return errs.Wrap(coreipc.DirectoryUnsafe, errs.WrapParams{},
			errs.String("rule", "searchable by anyone, and the lists open the socket to every account where the kernel names no peer"),
			errs.String("path", dir), errs.String("mode", info.Mode().Perm().String()))
	}
	return nil
}

// listen opens the Unix socket at cfg.Path, once its directory is private
// and what was at the path is known to be a leftover. The socket's mode and
// group are set between bind(2) and listen(2) (bindSocket, listenBound): no
// connection completes before the socket is at the posture the lists ask
// for, whatever the umask made of it at bind.
func listen(cfg *Config) (*Listener, error) {
	dir, self := filepath.Dir(cfg.Path), os.Geteuid()
	if err := prepareDir(dir); err != nil {
		return nil, err
	}
	p := socketPosture(cfg, self)
	if err := checkReach(dir, p, kernelNamesPeer); err != nil {
		return nil, err
	}
	if err := clearStale(cfg); err != nil {
		return nil, err
	}
	f, err := bindSocket(cfg.Path, p)
	if err != nil {
		return nil, err
	}
	ln, err := listenBound(f, cfg.Path)
	if err != nil {
		return nil, err
	}
	return &Listener{cfg: *cfg, ln: socketAcceptor{UnixListener: ln, groups: len(cfg.AllowGIDs) > 0}, self: self}, nil
}

// clearStale removes a socket nobody answers on, and refuses one somebody
// does. What is not a socket, or not ours, is never removed.
func clearStale(cfg *Config) error {
	info, err := os.Lstat(cfg.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return errs.Wrap(coreipc.ListenFailed, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("cause", err.Error()))
	case info.Mode()&os.ModeSocket == 0:
		return errs.Wrap(coreipc.DirectoryUnsafe, errs.WrapParams{}, errs.String("rule", "the socket path holds something that is not a socket"),
			errs.String("path", cfg.Path))
	}
	if uid, known := ownerOf(info); known && uid != os.Geteuid() {
		return errs.Wrap(coreipc.EndpointForeign, errs.WrapParams{}, errs.String("path", cfg.Path), errs.Int("uid", uid))
	}
	c, err := net.DialTimeout("unix", cfg.Path, dialTimeout)
	if err == nil {
		closeBestEffort(c, cfg.Path)
		return errs.Wrap(coreipc.InUse, errs.WrapParams{}, errs.String("path", cfg.Path))
	}
	//: only the kernel's word that nobody listens makes a socket a leftover. A
	//: live owner whose accept queue is full answers EAGAIN (Linux) or a dial
	//: that times out; reading that as "dead" removed a running server's socket
	//: and bound over it, and its clients reached the impostor.
	if !nobodyListens(err) {
		return errs.Wrap(coreipc.InUse, errs.WrapParams{}, errs.String("path", cfg.Path),
			errs.String("cause", "the socket's owner may be alive: "+err.Error()))
	}
	if err := os.Remove(cfg.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errs.Wrap(coreipc.ListenFailed, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("cause", err.Error()))
	}
	return nil
}

// nobodyListens reports whether a failed dial says the socket has no owner:
// ECONNREFUSED — a socket file no process listens on, what a killed daemon
// leaves — or ENOENT, the file gone between the Lstat and the dial. Any other
// failure, EAGAIN and a timeout first, leaves the owner possibly alive.
func nobodyListens(err error) bool {
	//: the two answers the kernel gives only when no listener exists.
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}

// admits is the Unix admission: this account, or an allowed user, or a peer
// whose effective or supplementary group is allowed, or a peer the kernel did
// not name where it names none — the directory admitted it.
func (l *Listener) admits(p coreipc.PeerValue, groups []int) error {
	return admit(p, groups, l.self, &l.cfg)
}

// dial connects to the Unix socket at cfg.Path, once the path to its
// directory is known to be steered by nobody else, the directory to be this
// account's and its file this account's (or an allowed one's). A directory
// that does not exist is nobody listening — the first client of a daemon not
// started yet —, DIAL_FAILED, not an unsafe directory.
func dial(ctx context.Context, cfg *Config) (*coreipc.Conn, error) {
	return dialWith(ctx, cfg, os.Lstat, os.Geteuid())
}

// dialWith is dial with the file-owner reading and the effective uid
// injected, so a test can stand on either side of an account it cannot be.
// The directory may be another account's (#1092, [checkDialDir]); the socket
// file may be owned by this account, an allowed user or an allowed group, and
// is ENDPOINT_FOREIGN otherwise.
func dialWith(ctx context.Context, cfg *Config, lstat func(string) (os.FileInfo, error), euid int) (*coreipc.Conn, error) {
	dir := filepath.Dir(cfg.Path)
	if _, err := lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, errs.Wrap(coreipc.DialFailed, errs.WrapParams{}, errs.String("path", cfg.Path),
			errs.String("cause", "nobody listens: the directory does not exist"))
	}
	if err := checkDialDir(dir, lstat); err != nil {
		return nil, err
	}
	if info, err := lstat(cfg.Path); err == nil {
		if uid, gid, known := ownersOf(info); known && !endpointAdmitted(uid, gid, euid, cfg) {
			return nil, errs.Wrap(coreipc.EndpointForeign, errs.WrapParams{}, errs.String("path", cfg.Path),
				errs.Int("uid", uid), errs.Int("gid", gid))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", cfg.Path)
	if err != nil {
		return nil, errs.Wrap(coreipc.DialFailed, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("cause", err.Error()))
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		closeBestEffort(c, cfg.Path)
		return nil, errs.Wrap(coreipc.DialFailed, errs.WrapParams{}, errs.String("path", cfg.Path), errs.String("cause", "not a unix connection"))
	}
	p := peerOf(uc)
	var groups []int
	if len(cfg.AllowGIDs) > 0 {
		groups = peerGroups(uc)
	}
	if err := admit(p, groups, euid, cfg); err != nil {
		closeBestEffort(c, cfg.Path)
		return nil, err
	}
	return &coreipc.Conn{Conn: withFiles(uc, p), Peer: p}, nil
}
