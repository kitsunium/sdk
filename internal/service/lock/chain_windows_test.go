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

// TestAnIndirectionAboveTheLockFileIsAcceptedOnWindows is the ACCEPTING half
// of the Windows chain rule, and it is the half that is easy to lose.
//
// checkChain refuses an indirection at a parent component only when the
// directory holding it is writable by anyone — which on Windows is a DACL
// question (dacl_windows.go), not a mode question, because os.Stat synthesises
// 0777 for every writable directory here. A rule that read that synthesised
// mode would refuse EVERY junction under a writable directory, including
// C:\Users\All Users -> C:\ProgramData, which Windows installs itself.
//
// So a junction in a directory only its owner can write is ACCEPTED, and the
// refusing half lives beside the rest of the DACL table in
// dacl_windows_test.go.
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
	//: a FAILURE and not a skip, which is where this row departs from
	//: nofollow_windows_test.go's shape. That table pairs a symbolic link with
	//: a junction precisely because the link needs
	//: SeCreateSymbolicLinkPrivilege and may legitimately be unavailable, and
	//: it fails only if BOTH are lost. This row has no pair: `mklink /J` needs
	//: no privilege at all, so a failure here is a change in the runner image,
	//: and skipping would leave the only assertion about this rule on this
	//: platform silently absent — indistinguishable from a pass, because
	//: e2e-cross runs `go test` without -v and discards a passing package's
	//: output (ADR 0082 §D5).
	if linkErr != nil {
		t.Fatalf("mklink /J failed on this runner — this is the only assertion the Windows chain rule has, so it is a failure and not a skip: %s (%v)", out, linkErr)
	}
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: filepath.Join(planted, "locks")})
	//: accepted, deliberately. A refusal here would mean the rule started
	//: reading the synthesised mode, which refuses every writable directory.
	if err != nil || locker == nil {
		t.Fatalf("NewFileLocker under a junction in an owner-only directory = %v, want a locker", err)
	}
}
