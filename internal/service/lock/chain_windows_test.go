//go:build windows

package lock_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// This file carries the SAME build constraint as dirsafety_windows.go, because
// what it pins is that file's `plantable` — and the verdict is the opposite of
// the POSIX one for a reason that is written down rather than implied.
//
// The lane that executes it is the `windows` job of
// .github/workflows/e2e-cross.yml. The Linux Bazel gate compiles neither
// dirsafety_windows.go nor this file.

// TestAnIndirectionAboveTheLockFileIsAcceptedOnWindows pins the gap, so that
// it is a measured decision rather than an untested assumption.
//
// checkChain refuses an indirection at a parent component only when the
// directory holding it is writable by anyone. On Unix that is one mode bit.
// On Windows there is no such bit to read: os.Stat synthesises the permission
// bits from FILE_ATTRIBUTE_READONLY, so every writable directory reports 0777
// and the rule would refuse EVERY junction under one — including
// C:\Users\All Users -> C:\ProgramData, which Windows installs itself.
//
// So `plantable` answers no here, checkChain refuses nothing, and this test
// says so out loud. The question does have an answer on this platform — the
// directory's DACL — and it is deferred with its price in ADR 0081
// §Alternatives and ADR 0083 §Deferred. When that lands, this test is the one
// that has to change, which is the point of writing it.
//
// A junction is planted rather than a symbolic link because `mklink /J` needs
// no privilege at all, so this row runs on an ordinary runner.
func TestAnIndirectionAboveTheLockFileIsAcceptedOnWindows(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(filepath.Join(target, "locks"), 0o700); err != nil {
		t.Fatalf("building the redirect target = %v", err)
	}
	planted := filepath.Join(base, "middle")
	out, linkErr := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", planted, target).CombinedOutput()
	//: the command's own output is the diagnosis; an exit status says nothing
	//: about why.
	if linkErr != nil {
		t.Skipf("cannot create a junction on this runner: %s (%v)", out, linkErr)
	}
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: filepath.Join(planted, "locks")})
	//: accepted, deliberately. A refusal here would mean `plantable` started
	//: reading the synthesised mode, which refuses every writable directory.
	if err != nil || locker == nil {
		t.Fatalf("NewFileLocker under a junction = %v, want a locker until the DACL check lands (ADR 0083 §Deferred)", err)
	}
}
