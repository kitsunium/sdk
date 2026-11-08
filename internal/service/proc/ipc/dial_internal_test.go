//go:build unix

package ipc

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakeInfo is a FileInfo an account other than the test's owns.
type fakeInfo struct {
	mode     fs.FileMode
	uid, gid uint32
}

func (f fakeInfo) Name() string       { return "fake" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid, Gid: f.gid} }

// TestDialAnotherAccountsSocket pins #1092's rows as the dialler with euid
// 1001 sees user 1000's daemon: a 0750 directory owned by 1000:3000 and a
// socket file 1000:3000. With AllowGIDs [3000] or AllowUIDs [1000] the dial
// gets past both owner rules (here to the connect, nobody listening); with
// empty allow-lists it is ENDPOINT_FOREIGN carrying uid 1000 — never
// DIRECTORY_UNSAFE; a 0770 or 0777 directory still is.
func TestDialAnotherAccountsSocket(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "daemon.sock")
	lstatAs := func(dirMode fs.FileMode) func(string) (os.FileInfo, error) {
		return func(name string) (os.FileInfo, error) {
			switch name {
			case dir:
				return fakeInfo{mode: fs.ModeDir | dirMode, uid: 1000, gid: 3000}, nil
			case socket:
				return fakeInfo{mode: fs.ModeSocket | 0o660, uid: 1000, gid: 3000}, nil
			default:
				return os.Lstat(name)
			}
		}
	}
	ctx := context.Background()
	for _, cfg := range []Config{{Path: socket, AllowGIDs: []int{3000}}, {Path: socket, AllowUIDs: []int{1000}}} {
		_, err := dialWith(ctx, &cfg, lstatAs(0o750), 1001)
		//: both owner rules passed: the connect is what failed.
		if !errs.HasCode(err, coreipc.CodeDialFailed) {
			t.Fatalf("dial with %+v = %v, want past the owner rules (DIAL_FAILED: nobody listens)", cfg, err)
		}
	}
	_, err := dialWith(ctx, &Config{Path: socket}, lstatAs(0o750), 1001)
	if !errs.HasCode(err, coreipc.CodeEndpointForeign) {
		t.Fatalf("dial with no allow-list = %v, want ENDPOINT_FOREIGN", err)
	}
	for _, unsafe := range []fs.FileMode{0o770, 0o777} {
		_, err := dialWith(ctx, &Config{Path: socket, AllowGIDs: []int{3000}}, lstatAs(unsafe), 1001)
		if !errs.HasCode(err, coreipc.CodeDirectoryUnsafe) {
			t.Fatalf("dial into a %o directory = %v, want DIRECTORY_UNSAFE", unsafe, err)
		}
	}
}
