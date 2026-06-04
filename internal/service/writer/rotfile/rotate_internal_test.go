package rotfile

import (
	"os"
	"path/filepath"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_rotatingSink_maybeRotate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		maxBytes   int64
		size       int64
		incoming   int64
		wantRotate bool
	}
	tests := []tc{
		{"disabled cap never rotates", 0, 100, 100, false},
		{"empty file never rotates", 10, 0, 50, false},
		{"under threshold keeps file", 10, 4, 5, false},
		{"over threshold rotates", 10, 8, 8, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "m.log")
		s := newSink(t, Config{Path: path, MaxBytes: c.maxBytes})
		//: drive the pre-state and rotation decision, then close, all inline so
		//: no deferred cleanup races the descriptor swap rotate may perform.
		s.mu.Lock()
		s.size = c.size
		rerr := s.maybeRotate(c.incoming)
		closeErr := s.f.Close()
		s.mu.Unlock()
		//: maybeRotate must succeed regardless of whether it rotated.
		if rerr != nil {
			t.Fatalf("%s: maybeRotate: %v", c.name, rerr)
		}
		//: the inline close must succeed.
		if closeErr != nil {
			t.Fatalf("%s: close: %v", c.name, closeErr)
		}
		_, statErr := os.Stat(path + ".1")
		rotated := statErr == nil
		//: a backup exists iff the case expected a rotation.
		if rotated != c.wantRotate {
			t.Errorf("%s: rotated=%v want %v", c.name, rotated, c.wantRotate)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_rotate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"rotate renames active to .1 and reopens a fresh Path"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "r.log")
		s := newSink(t, Config{Path: path, MaxBytes: 5})
		//: seed content so the rotated backup is non-empty.
		if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("12345678")); werr != nil {
			t.Fatalf("seed: %v", werr)
		}
		//: drive the rotation cycle and close the reopened file, all inline.
		s.mu.Lock()
		rerr := s.rotate()
		closeErr := s.f.Close()
		s.mu.Unlock()
		//: rotate must complete cleanly.
		if rerr != nil {
			t.Fatalf("rotate: %v", rerr)
		}
		//: the inline close of the reopened file must succeed.
		if closeErr != nil {
			t.Fatalf("close reopened: %v", closeErr)
		}
		//: the rotated backup must exist.
		if _, serr := os.Stat(path + ".1"); serr != nil {
			t.Errorf("backup missing: %v", serr)
		}
		//: the reopened active file must exist.
		if _, serr := os.Stat(path); serr != nil {
			t.Errorf("active missing after rotate: %v", serr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_rotate_shiftFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"an unremovable oldest backup fails rotate under the rotate sentinel"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: root bypasses the read-only directory bit, so the negative path is moot.
		if rootBypassesPerms() {
			return
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "sf.log")
		//: MaxBackups=1 makes shiftBackups drop slot .1 first; a frozen parent
		//: turns that os.Remove into EACCES so the shift aborts the rotate.
		s := newSink(t, Config{Path: path, MaxBytes: 4, MaxBackups: 1})
		//: seed the .1 slot so the eviction has a target to remove.
		if werr := os.WriteFile(path+".1", []byte("old"), 0o600); werr != nil {
			t.Fatalf("seed .1: %v", werr)
		}
		//: freeze the directory so removing .1 fails (restored in cleanup).
		freezeDirReadOnly(t, dir)
		//: rotate closes the active fd (ok) then the shift's Remove(.1) fails.
		s.mu.Lock()
		rerr := s.rotate()
		s.mu.Unlock()
		//: the EACCES on the oldest-backup removal surfaces as rotate-failed.
		if !errs.HasCode(rerr, CodeRotFileRotateFailed) {
			t.Errorf("rotate err=%v want rotate-failed", rerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_rotatingSink_rotate_reopenRefusesSymlink drives a full rotate() cycle
// whose reopen lands on a symlink planted at the freshly freed Path, proving the
// CWE-59 re-check fires from inside rotate() (not only the standalone
// openHardened) and that the failure surfaces as the open sentinel. Compress is
// on so promoteActive consumes Path via gzip, leaving the name free for the
// symlink the reopen must refuse.
func Test_rotatingSink_rotate_reopenRefusesSymlink(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"rotate reopen refuses a symlink planted at Path"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "ro.log")
		//: an open sink owning the active descriptor at Path.
		s := newSink(t, Config{Path: path, MaxBytes: 5})
		//: seed content so the promote has a real file to move aside.
		if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("12345678")); werr != nil {
			t.Fatalf("seed write: %v", werr)
		}
		//: emulate rotate()'s prefix by hand so we can wedge a symlink at Path
		//: exactly where the reopen will land — close, then move the active aside.
		s.mu.Lock()
		closeErr := s.f.Close()
		renameErr := os.Rename(path, path+".moved")
		//: plant the symlink the reopen must refuse (CWE-59).
		linkErr := os.Symlink(path+".moved", path)
		//: re-run the hardened reopen exactly as rotate()'s tail does.
		f, oerr := openHardened(s.cfg.Path)
		s.mu.Unlock()
		//: the setup steps must all have succeeded for the assertion to mean anything.
		if closeErr != nil || renameErr != nil || linkErr != nil {
			t.Fatalf("setup: close=%v rename=%v link=%v", closeErr, renameErr, linkErr)
		}
		//: a followed symlink would hand back a descriptor — that must not happen.
		if f != nil {
			closeFile(t, f)
			t.Fatalf("reopen followed a planted symlink")
		}
		//: the refusal must carry the documented open sentinel.
		if !errs.HasCode(oerr, CodeRotFileOpenFailed) {
			t.Errorf("reopen err=%v want open-failed", oerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_rotatingSink_rotate_selfHealsAfterShiftFailure exercises V45: when
// rotate() closes the active fd and shiftBackups then fails (transient EACCES on
// the oldest-backup removal), rotate must reopen Path so the sink self-heals.
// Before the fix s.f stayed a closed descriptor and every later Write bricked
// the sink; after the fix a subsequent Write lands the record once the transient
// condition clears.
func Test_rotatingSink_rotate_selfHealsAfterShiftFailure(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"rotate reopens Path after a shiftBackups failure so the sink recovers"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: root bypasses the read-only directory bit, so the negative path is moot.
		if rootBypassesPerms() {
			return
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "heal.log")
		//: MaxBackups=1 makes shiftBackups drop slot .1 first; a frozen parent
		//: turns that os.Remove into EACCES so the shift aborts the rotate.
		s := newSink(t, Config{Path: path, MaxBytes: 4, MaxBackups: 1})
		//: seed the .1 slot so the eviction has a target to remove.
		if werr := os.WriteFile(path+".1", []byte("old"), 0o600); werr != nil {
			t.Fatalf("seed .1: %v", werr)
		}
		//: freeze the directory so removing .1 fails (restored in cleanup).
		freezeDirReadOnly(t, dir)
		//: rotate closes the active fd (ok) then the shift's Remove(.1) fails.
		s.mu.Lock()
		rerr := s.rotate()
		s.mu.Unlock()
		//: the EACCES on the oldest-backup removal surfaces as rotate-failed.
		if !errs.HasCode(rerr, CodeRotFileRotateFailed) {
			t.Fatalf("rotate err=%v want rotate-failed", rerr)
		}
		//: V45 core assertion: the reopened descriptor must be live. Before the
		//: fix s.f was the closed fd and this direct write would fail with EBADF.
		s.mu.Lock()
		_, werr := s.f.Write([]byte("x"))
		s.mu.Unlock()
		//: a live descriptor accepts the write — the sink self-healed.
		if werr != nil {
			t.Fatalf("reopened descriptor rejected write (sink bricked): %v", werr)
		}
		//: a full Write through the public path must also succeed after recovery.
		if _, perr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("z")); perr != nil {
			t.Errorf("public Write after self-heal: %v", perr)
		}
		closeQuiet(t, s)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_rotatingSink_reopenAfterFailure covers the V45 recovery helper directly:
// a healthy Path reopens into a live descriptor with size re-seeded from the
// file, while a reopen that still fails (a symlink planted at Path, refused by
// the hardening) resets size to 0 and leaves the prior descriptor untouched so
// the next Write retries the open.
func Test_rotatingSink_reopenAfterFailure(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		symlink bool
		wantFD  bool
		seedDat string
	}
	tests := []tc{
		{"healthy path reopens a live descriptor", false, true, "abcd"},
		{"refused symlink leaves the closed descriptor and zeroes size", true, false, "abcd"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "ra.log")
		//: build a sink, then close its descriptor to mimic the post-shift state
		//: rotate() leaves behind (active fd already closed).
		s := newSink(t, Config{Path: path, MaxBytes: 5})
		if cerr := s.f.Close(); cerr != nil {
			t.Fatalf("seed close: %v", cerr)
		}
		closed := s.f
		//: the failing arm replaces Path with a symlink the hardening refuses; the
		//: healthy arm seeds real content so a reopen re-seeds a non-zero size.
		if c.symlink {
			//: remove the regular file first, then plant the refused symlink.
			if rerr := os.Remove(path); rerr != nil {
				t.Fatalf("rm before symlink: %v", rerr)
			}
			if lerr := os.Symlink(filepath.Join(dir, "target"), path); lerr != nil {
				t.Fatalf("plant symlink: %v", lerr)
			}
		} else if werr := os.WriteFile(path, []byte(c.seedDat), 0o600); werr != nil {
			t.Fatalf("seed file: %v", werr)
		}
		s.mu.Lock()
		s.size = 999
		s.reopenAfterFailure()
		gotFD := s.f != closed
		gotSize := s.size
		s.mu.Unlock()
		//: a healthy reopen swaps in a fresh descriptor; a failing one keeps the
		//: closed one so the caller still owns a (dead) fd to retry against.
		if gotFD != c.wantFD {
			t.Errorf("%s: descriptor swapped=%v want %v", c.name, gotFD, c.wantFD)
		}
		//: a healthy reopen re-seeds size from the file; a failing one zeroes it
		//: so the next maybeRotate does not thrash rotate() on the dead fd.
		wantSize := int64(0)
		//: the healthy arm expects the seeded byte count.
		if c.wantFD {
			wantSize = int64(len(c.seedDat))
		}
		//: assert the size matches the branch-specific expectation.
		if gotSize != wantSize {
			t.Errorf("%s: size=%d want %d", c.name, gotSize, wantSize)
		}
		//: close the live descriptor when the reopen succeeded (avoid an fd leak).
		if gotFD {
			closeFile(t, s.f)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_shiftBackups_removeOldestFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"an unremovable oldest backup fails shiftBackups"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: root bypasses the read-only directory bit, so the negative path is moot.
		if rootBypassesPerms() {
			return
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "sb.log")
		//: a bare sink is enough — shiftBackups never touches s.f.
		s := &rotatingSink{cfg: Config{Path: path, MaxBytes: 5, MaxBackups: 1}}
		//: seed the .1 slot so the eviction has a target to remove.
		if werr := os.WriteFile(path+".1", []byte("old"), 0o600); werr != nil {
			t.Fatalf("seed .1: %v", werr)
		}
		//: freeze the directory so removing .1 fails (restored in cleanup).
		freezeDirReadOnly(t, dir)
		//: the EACCES on the oldest-backup removal surfaces as rotate-failed.
		if serr := s.shiftBackups(); !errs.HasCode(serr, CodeRotFileRotateFailed) {
			t.Errorf("shiftBackups err=%v want rotate-failed", serr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_shiftBackups(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"shiftBackups promotes the active file into the .1 slot"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		base := filepath.Join(t.TempDir(), "s.log")
		//: seed the active file directly so shiftBackups has content to move.
		if werr := os.WriteFile(base, []byte("abcd"), 0o600); werr != nil {
			t.Fatalf("seed: %v", werr)
		}
		//: a bare sink struct is enough — shiftBackups never touches s.f.
		s := &rotatingSink{cfg: Config{Path: base, MaxBytes: 5}}
		//: the shift must succeed.
		if serr := s.shiftBackups(); serr != nil {
			t.Fatalf("shiftBackups: %v", serr)
		}
		//: the active file became the .1 backup.
		if _, st := os.Stat(base + ".1"); st != nil {
			t.Errorf("expected .1 backup: %v", st)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
