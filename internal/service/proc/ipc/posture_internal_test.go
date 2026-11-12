//go:build unix

package ipc

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTheSocketPostureIsTheNarrowestTheListsNeed pins #1092's table: the
// lists that name nobody but this account and root keep 0600; one group, and
// no other user, gives 0660 and that group; another user or several groups
// give 0666, and only those say that every account may connect.
func TestTheSocketPostureIsTheNarrowestTheListsNeed(t *testing.T) {
	t.Parallel()
	const self = 1000
	for _, r := range []struct {
		name string
		cfg  Config
		want posture
	}{
		{"no list", Config{}, posture{mode: 0o600, gid: -1}},
		{"this account and root", Config{AllowUIDs: []int{self, 0}}, posture{mode: 0o600, gid: -1}},
		{"one group", Config{AllowGIDs: []int{3000}}, posture{mode: 0o660, gid: 3000}},
		{"one group twice, and root", Config{AllowUIDs: []int{0}, AllowGIDs: []int{3000, 3000}}, posture{mode: 0o660, gid: 3000}},
		{"another user", Config{AllowUIDs: []int{1001}}, posture{mode: 0o666, gid: -1, others: true}},
		{"two groups", Config{AllowGIDs: []int{3001, 3000}}, posture{mode: 0o666, gid: -1, others: true}},
		{"a user and a group", Config{AllowUIDs: []int{1001}, AllowGIDs: []int{3000}}, posture{mode: 0o666, gid: -1, others: true}},
	} {
		if got := socketPosture(&r.cfg, self); got != r.want {
			t.Errorf("%s: socketPosture = %+v, want %+v", r.name, got, r.want)
		}
	}
}

// TestASocketOpenToEveryoneNeedsADirectoryThatFilters: where the kernel names
// no peer, a 0666 socket in a directory anybody can search would admit
// anybody — refused, DIRECTORY_UNSAFE; a 0750 directory filters. Where the
// kernel names peers the admission still gates, and either directory goes.
func TestASocketOpenToEveryoneNeedsADirectoryThatFilters(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	open := posture{mode: 0o666, gid: -1, others: true}
	for _, r := range []struct {
		mode  os.FileMode
		named bool
		p     posture
		ok    bool
	}{
		{0o755, false, open, false},
		{0o711, false, open, false},
		{0o750, false, open, true},
		{0o755, true, open, true},
		{0o755, false, posture{mode: 0o660, gid: 3000}, true},
	} {
		dir := filepath.Join(base, r.mode.String()+map[bool]string{true: "-named", false: "-unnamed"}[r.named]+map[bool]string{true: "-open", false: "-group"}[r.p.others])
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, r.mode); err != nil {
			t.Fatal(err)
		}
		err := checkReach(dir, r.p, r.named)
		if r.ok != (err == nil) || (err != nil && !errs.HasCode(err, coreipc.CodeDirectoryUnsafe)) {
			t.Errorf("checkReach(%o, named %v, %+v) = %v, want ok=%v", r.mode, r.named, r.p, err, r.ok)
		}
	}
}

// TestTheModeIsSetBeforeTheSocketListens: between bindSocket and listenBound
// the file already has its posture's mode and group, and a connect is refused
// — no connection is made while the umask's mode still stood — then it is
// accepted once listening, and closing the listener removes the file.
func TestTheModeIsSetBeforeTheSocketListens(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "d.sock")
	if len(path) > maxPath {
		t.Skipf("the test's directory is too deep for a socket path (%d bytes)", len(path))
	}
	f, err := bindSocket(path, posture{mode: 0o660, gid: os.Getegid()})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o660 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("the bound socket = %v, want a 0660 socket before it listens", info.Mode())
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Gid) != os.Getegid() {
		t.Fatalf("the bound socket's group = %d, want %d", st.Gid, os.Getegid())
	}
	if c, err := net.Dial("unix", path); err == nil {
		closeBestEffort(c, path)
		t.Fatal("a connect before listen(2) succeeded, want ECONNREFUSED")
	} else if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("a connect before listen(2) = %v, want ECONNREFUSED", err)
	}
	ln, err := listenBound(f, path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("a connect once listening = %v", err)
	}
	closeBestEffort(c, path)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket file after Close = %v, want it removed", err)
	}
}
