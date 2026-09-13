//go:build unix

// Package file — the proof that the OPEN refuses an indirection, and not only
// the os.Lstat that ran a moment before it.
//
// TestNew_RejectsSymlink plants the link and then calls New, so refuseSymlink
// answers first and the flag word is never asked anything: that case passes
// identically with and without O_NOFOLLOW. This file puts the question to the
// flag word directly — what happens when the link appears AFTER the check has
// already looked, which is the one state the package's own documentation says
// O_NOFOLLOW exists to cover.
package file

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// seededContent is planted in the link's target so a followed open can be seen
// landing on it. The exact bytes are read back, because "the open failed" is a
// weaker sentence than "the target is byte-for-byte what it was".
const seededContent string = "DATA THAT MATTERS\n"

// followMarker is what a followed open is made to write. A descriptor on its
// own does not say where it landed; this does.
const followMarker string = "the log sink appended here\n"

// plantLink creates link -> target and reports whether the filesystem allowed
// it. A row that could not be planted verifies nothing, so it returns false
// instead of passing; the caller counts the rows that WERE planted and fails
// when that count is zero, so a host without symbolic links says so in red
// rather than going green on an empty table.
func plantLink(t *testing.T, target, link string) bool {
	t.Helper()
	lerr := os.Symlink(target, link)
	//: no link, no row — and the caller's planted counter stays put.
	if lerr != nil {
		t.Logf("cannot plant a symbolic link on this filesystem (%v); row not run", lerr)
		return false
	}
	return true
}

// reportFollowed turns a successful open into evidence of what it opened: it
// writes through the descriptor and reads the target back, so the failure names
// the file the sink was never pointed at rather than saying "open succeeded".
func reportFollowed(t *testing.T, f io.WriteCloser, link, target string) {
	t.Helper()
	_, werr := io.WriteString(f, followMarker)
	cerr := f.Close()
	landed, rerr := os.ReadFile(target)
	t.Errorf("the open FOLLOWED the link planted at %s (write=%v close=%v): %s now holds %q (read=%v) — "+
		"openFlags carries no O_NOFOLLOW on %s/%s, so refuseSymlink's os.Lstat is the only protection "+
		"and the window between it and this open is wide open",
		link, werr, cerr, target, landed, rerr, runtime.GOOS, runtime.GOARCH)
}

// assertTargetUntouched checks that a refused open left the planted link's
// target exactly as it was. The dangling row is the sharper of the two: a
// followed O_CREATE would have CREATED the target, so its continued absence is
// positive proof of non-traversal rather than the mere absence of a descriptor.
func assertTargetUntouched(t *testing.T, dangling bool, target string) {
	t.Helper()
	content, rerr := os.ReadFile(target)
	//: dangling row — the target must still not exist.
	if dangling {
		//: anything other than "not found" means the open created it.
		if !os.IsNotExist(rerr) {
			t.Errorf("the refused open still created %s (read err=%v, content=%q)", target, rerr, content)
		}
		return
	}
	//: live row — the target must be byte-for-byte what it was seeded with.
	if rerr != nil || string(content) != seededContent {
		t.Errorf("target %s changed under a refused open: content=%q err=%v, want %q",
			target, content, rerr, seededContent)
	}
}

// nofollowCase is one planted-link row: the name, and whether the link points
// at a path that does not exist yet.
type nofollowCase struct {
	// name labels the subtest.
	name string
	// dangling points the link at a path that does not exist yet, which is the
	// shape the attack actually takes: plant the link, let the victim's
	// O_CREATE make the file.
	dangling bool
}

// TestTheOpenRefusesALinkPlantedAfterTheLstatCheck opens with exactly the flag
// word and mode New uses, against a path that IS a symbolic link — the state a
// planter leaves behind by winning the race against refuseSymlink. It is not an
// artificial state: it is the one the package's own documentation says
// O_NOFOLLOW exists to cover.
//
// The errno is deliberately never inspected. O_NOFOLLOW reports a planted link
// as ELOOP on linux, openbsd and darwin, EMLINK on freebsd and dragonfly, and
// EFTYPE on netbsd (ADR 0082 §D4); a test that named one of them would be wrong
// on four kernels. What is asserted is that no descriptor came back and that
// the target was not touched.
func TestTheOpenRefusesALinkPlantedAfterTheLstatCheck(t *testing.T) {
	tests := []nofollowCase{
		{"symbolic link to an existing file", false},
		{"dangling symbolic link", true},
	}
	planted := 0
	runCase := func(t *testing.T, c nofollowCase) {
		t.Helper()
		dir := t.TempDir()
		link := filepath.Join(dir, "app.log")
		target := filepath.Join(dir, "victim.txt")
		//: the live row seeds the target so a follow can be seen writing to
		//: it; the dangling row leaves it absent so a follow CREATES it.
		if !c.dangling {
			//: a seed that fails makes the row meaningless, so stop here.
			if werr := os.WriteFile(target, []byte(seededContent), defaultFilePerm); werr != nil {
				t.Fatalf("seed target: %v", werr)
			}
		}
		//: an unplantable row is not a passing row.
		if !plantLink(t, target, link) {
			return
		}
		planted++
		f, oerr := os.OpenFile(link, openFlags, defaultFilePerm)
		//: a descriptor means the kernel traversed the link.
		if oerr == nil {
			reportFollowed(t, f, link, target)
			return
		}
		assertTargetUntouched(t, c.dangling, target)
	}
	//: sequential on purpose — the rows share the planted counter, and the
	//: accounting is worth more than the milliseconds parallelism would save.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
	//: zero planted rows means nothing was verified; say so rather than pass.
	if planted == 0 {
		t.Fatalf("no row could be planted on this filesystem, so nothing verified that the open "+
			"refuses an indirection — O_NOFOLLOW is UNPROVEN on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}
