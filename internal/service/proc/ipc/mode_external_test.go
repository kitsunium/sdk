//go:build unix

package ipc_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/ipc"
)

// tempSocket is the socket path d.sock in the directory sub of dir, a
// directory of the test's own, or a skip when that is too deep for sun_path
// (macOS's /var/folders).
func tempSocket(t *testing.T, dir, sub string) string {
	t.Helper()
	path := filepath.Join(dir, sub, "d.sock")
	if len(path) > 103 {
		t.Skipf("the test's directory is too deep for a socket path (%d bytes)", len(path))
	}
	return path
}

// foreignUID is a uid that is neither this process's nor root's.
func foreignUID() int {
	if os.Geteuid() == 65534 {
		return 65533
	}
	return 65534
}

// TestTheSocketModeFollowsTheAllowLists pins #1092's mode (the owner's
// decision of 2026-11-10): the socket stays 0600 when nothing but this
// account and root are named; a single group gets 0660 and that group, so the
// kernel itself lets its members — supplementary ones included — connect and
// nobody else; another user, or several groups, get 0666 under the directory
// that gates them. Before #1092 every row was 0600, and no account the lists
// named could connect(2).
func TestTheSocketModeFollowsTheAllowLists(t *testing.T) {
	t.Parallel()
	self, dir := os.Geteuid(), t.TempDir()
	for i, r := range []struct {
		name string
		cfg  ipc.Config
		mode os.FileMode
		gid  int
	}{
		{"no list", ipc.Config{}, 0o600, -1},
		{"this account and root", ipc.Config{AllowUIDs: []int{self, 0}}, 0o600, -1},
		{"this process's group", ipc.Config{AllowGIDs: []int{os.Getegid()}}, 0o660, os.Getegid()},
		{"another user", ipc.Config{AllowUIDs: []int{foreignUID()}}, 0o666, -1},
		{"two groups", ipc.Config{AllowGIDs: []int{os.Getegid(), os.Getegid() + 1}}, 0o666, -1},
	} {
		sub := strconv.Itoa(i)
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			cfg := r.cfg
			cfg.Path = tempSocket(t, dir, sub)
			ln, err := ipc.NewListener(&cfg)
			if err != nil {
				t.Fatalf("NewListener(%+v) = %v", r.cfg, err)
			}
			defer closeOrLog(t, ln)
			info, err := os.Lstat(cfg.Path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != r.mode {
				t.Errorf("the socket's mode = %o, want %o", info.Mode().Perm(), r.mode)
			}
			if st, ok := info.Sys().(*syscall.Stat_t); ok && r.gid >= 0 && int(st.Gid) != r.gid {
				t.Errorf("the socket's group = %d, want %d", st.Gid, r.gid)
			}
			c, err := ipc.Dial(t.Context(), &cfg)
			if err != nil {
				t.Fatalf("this account's own dial = %v", err)
			}
			closeOrLog(t, c)
		})
	}
}

// TestAGroupTheSocketCannotTakeIsRefused: a single allowed group the socket
// cannot be given — this account is not a member, and is not root — is
// refused at Listen (LISTEN_FAILED) rather than left at 0600, where the group
// could never connect, or opened to everyone; nothing stays at the path.
func TestAGroupTheSocketCannotTakeIsRefused(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root may give a file any group")
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Skipf("this process's groups cannot be read: %v", err)
	}
	gid := 65533
	for slices.Contains(groups, gid) || gid == os.Getegid() {
		gid--
	}
	cfg := ipc.Config{Path: tempSocket(t, t.TempDir(), "run"), AllowGIDs: []int{gid}}
	ln, err := ipc.NewListener(&cfg)
	if err == nil {
		closeOrLog(t, ln)
		t.Fatalf("NewListener with AllowGIDs [%d], a group this account is not in = nil, want LISTEN_FAILED", gid)
	}
	if !errs.HasCode(err, coreipc.CodeListenFailed) {
		t.Fatalf("NewListener with AllowGIDs [%d] = %v, want LISTEN_FAILED", gid, err)
	}
	if _, statErr := os.Lstat(cfg.Path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the refused socket is still at the path: %v", statErr)
	}
}
