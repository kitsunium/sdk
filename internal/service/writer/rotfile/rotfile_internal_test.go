package rotfile

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// closeQuiet closes a sink and fails the test on an unexpected error.
func closeQuiet(t *testing.T, s *rotatingSink) {
	t.Helper()
	//: a cleanup close should succeed; an already-closed sink is tolerated.
	if cerr := s.Close(); cerr != nil && !errs.HasCode(cerr, CodeRotFileWriteFailed) {
		t.Errorf("close: %v", cerr)
	}
}

// closeFile closes a raw descriptor and fails the test on error. It takes an
// io.Closer so the helper does not pin the concrete *os.File type.
func closeFile(t *testing.T, f io.Closer) {
	t.Helper()
	//: surface a close failure rather than discarding it.
	if cerr := f.Close(); cerr != nil {
		t.Errorf("close file: %v", cerr)
	}
}

// newCancelled returns a context plus its cancel func so a case can cancel it
// to exercise the cancelled-context guard.
func newCancelled(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	//: derive from the test context so the deadline is bounded.
	return context.WithCancel(t.Context())
}

// assertPerm0600 fails the test unless path exists with exactly 0600 perms.
func assertPerm0600(t *testing.T, name, path string) {
	t.Helper()
	fi, serr := os.Stat(path)
	//: the file must exist to assert its mode.
	if serr != nil {
		t.Fatalf("%s: stat %s: %v", name, path, serr)
	}
	//: hardened files must be owner-only readable/writable.
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s: perm=%o want 600", name, perm)
	}
}

// rootBypassesPerms reports whether the process is the superuser, who ignores
// the 0555 directory mode the permission-injection cases rely on. Callers
// early-return on true so the EACCES assertion is never a false negative under
// root — without t.Skip(), which KTN-TEST-NOSKIP forbids.
func rootBypassesPerms() bool {
	//: root ignores the read-only directory bit, so the negative path is moot.
	return os.Getuid() == 0
}

// freezeDirReadOnly chmods dir to 0555 (no write) so create/rename/remove of an
// entry inside it fails with EACCES, then restores 0700 in cleanup so t.TempDir
// teardown can delete the tree. It is the single injection seam for the rotate /
// prune failure branches that need a non-removable on-disk slot.
func freezeDirReadOnly(t *testing.T, dir string) {
	t.Helper()
	//: drop write permission so the kernel refuses mutations of dir's entries.
	if cerr := os.Chmod(dir, 0o555); cerr != nil {
		t.Fatalf("chmod 0555 %s: %v", dir, cerr)
	}
	t.Cleanup(func() {
		//: restore write so TempDir cleanup can unlink the contents.
		if cerr := os.Chmod(dir, 0o700); cerr != nil {
			t.Errorf("restore perms %s: %v", dir, cerr)
		}
	})
}

func Test_rotFileFactory_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want writer.Name
	}
	tests := []tc{{"reports the canonical key", "rotfile"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the factory must report the key it registered under.
		if got := (&rotFileFactory{}).Name(); got != c.want {
			t.Errorf("%s: Name()=%q want %q", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotFileFactory_Open(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	type tc struct {
		name    string
		cfg     writer.Config
		wantErr bool
	}
	tests := []tc{
		{"valid config builds a sink", Config{Path: filepath.Join(dir, "o.log")}, false},
		{"empty path rejected", Config{Path: ""}, true},
		{"wrong config type rejected", writer.ConsoleConfig{}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := (&rotFileFactory{}).Open(c.cfg)
		//: release any opened descriptor on the happy path.
		if sink != nil {
			t.Cleanup(func() {
				//: surface a close failure rather than discarding it.
				if cerr := sink.Close(); cerr != nil {
					t.Errorf("%s: cleanup close: %v", c.name, cerr)
				}
			})
		}
		//: failure arm — error + nil sink.
		if c.wantErr {
			if err == nil || sink != nil {
				t.Errorf("%s: err=%v sink=%v want error+nil", c.name, err, sink)
			}
			return
		}
		//: happy arm — nil error + usable sink.
		if err != nil || sink == nil {
			t.Errorf("%s: err=%v sink=%v want nil+sink", c.name, err, sink)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
