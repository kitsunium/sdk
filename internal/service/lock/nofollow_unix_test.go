//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package lock_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// This file carries the SAME build constraint as nofollow_unix.go, so it runs
// on every kernel that compiles the O_NOFOLLOW open and nowhere else. The
// e2e-cross lane executes it on linux, darwin, freebsd, openbsd and netbsd —
// which is what turns "the errno differs per kernel" from a claim in a comment
// into five real refusals, because nothing asserted below names an errno.

// victimName is the lock every case here contends for. It is fixed so the
// digest that becomes the filename is fixed too, which is the property the
// attack rests on.
const victimName string = "victim"

// plantedDir returns a lock directory in the one shape this defect needs:
// 0777 with the sticky bit, the mode checkDir explicitly ACCEPTS and exactly
// what /tmp is.
//
// The mode is not decoration. The whole point is that the directory rule
// PASSES: an attacker who pre-creates the lock directory as a shared temporary
// directory looks like cleared every check this package had before O_NOFOLLOW.
func plantedDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "locks")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("creating the lock directory = %v", err)
	}
	//: Mkdir's mode is filtered by the umask, so the mode under test is set
	//: afterwards — otherwise a runner with umask 022 would silently make this
	//: an ordinary 0755 directory and the premise would be untested.
	if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil {
		t.Skipf("cannot set 0777|sticky on a directory here: %v", err)
	}
	info, statErr := os.Stat(dir)
	//: a filesystem that drops the sticky bit would make the constructor
	//: refuse for a reason that is not this test's subject.
	if statErr != nil || info.Mode()&os.ModeSticky == 0 {
		t.Skip("this filesystem does not honour the sticky bit on a directory")
	}
	return dir
}

// discoverLockPath returns the filename locker uses for [victimName], by
// acquiring once, releasing, and reading the directory.
//
// This IS the attack's reconnaissance step, written out rather than described:
// the filename is the SHA-256 of the lock name, which makes it unforgeable and
// entirely predictable, and predictable is all that is needed. The path is
// removed before it is returned, leaving the name free to be planted at.
func discoverLockPath(t *testing.T, locker corelock.Locker, dir string) string {
	t.Helper()
	lease, err := locker.Acquire(t.Context(), victimName)
	if err != nil {
		t.Fatalf("the reconnaissance Acquire = %v", err)
	}
	if releaseErr := lease.Release(t.Context()); releaseErr != nil {
		t.Fatalf("the reconnaissance Release = %v", releaseErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("reading the lock directory = (%v entries, %v), want exactly 1", len(entries), err)
	}
	path := filepath.Join(dir, entries[0].Name())
	//: and it is the path lockFilePath predicts, which makes that helper's
	//: copy of the mapping a pin rather than a second implementation.
	if want := lockFilePath(dir, victimName); path != want {
		t.Fatalf("the locker used %s, want %s", path, want)
	}
	if removeErr := os.Remove(path); removeErr != nil {
		t.Fatalf("removing the lock file = %v", removeErr)
	}
	return path
}

// symlinkOrSkip plants newname -> oldname, skipping where the filesystem will
// not carry a symbolic link at all.
func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("this filesystem refuses symbolic links: %v", err)
	}
}

// TestTheFileLockerRefusesAnIndirectionAtTheLockPath is the probe that found
// the defect, reduced to a table.
//
// Before the O_NOFOLLOW open, the "symlink to a file that does not exist" row
// produced this from an external program, against the locker as it shipped:
//
//	répertoire 0777|sticky : ACCEPTÉ
//	acquisition sur lien planté : held=true err=<nil>
//	SUIVI : le verrou a atterri sur /tmp/sym.../elsewhere.lock
//
// The acquisition SUCCEEDED and the lock landed outside the checked directory.
// Two processes each holding "the same" lock over different inodes is the
// exact failure this domain exists to prevent, and nothing reported anything.
//
// The accepting rows are here for the reason the directory-rule table has its
// own: a guard that refuses the obvious case while quietly refusing a
// legitimate one is a different bug wearing the same green tick. A hard link
// is NOT an indirection — the name resolves to a real inode and O_NOFOLLOW
// says nothing about it — so it must still work.
func TestTheFileLockerRefusesAnIndirectionAtTheLockPath(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// plant prepares the lock path, which has already been created once
		// and removed, so the row knows the exact name to plant at. It returns
		// the assertion that the acquisition left the redirect target ALONE —
		// which is the half the sentinel does not cover, because a refusal
		// that still wrote through the link is a refusal in name only.
		plant  func(t *testing.T, base, lockPath string) func(t *testing.T)
		refuse bool
	}
	tests := []tc{
		{
			name: "no indirection at all",
			plant: func(t *testing.T, _, _ string) func(t *testing.T) {
				t.Helper()
				//: nothing planted: the ordinary path, and the row that fails
				//: if O_NOFOLLOW ever refuses a plain file.
				return func(*testing.T) {}
			},
			refuse: false,
		},
		{
			name: "symlink to a file that does not exist",
			plant: func(t *testing.T, base, lockPath string) func(t *testing.T) {
				t.Helper()
				target := filepath.Join(base, "elsewhere.lock")
				symlinkOrSkip(t, target, lockPath)
				//: the shape the attack actually takes: the VICTIM creates the
				//: target, so the attacker needs no access to it beforehand.
				return func(t *testing.T) {
					t.Helper()
					if _, err := os.Lstat(target); err == nil {
						t.Fatalf("the refused acquisition still created %s", target)
					}
				}
			},
			refuse: true,
		},
		{
			name: "symlink to an existing decimal file",
			plant: func(t *testing.T, base, lockPath string) func(t *testing.T) {
				t.Helper()
				target := filepath.Join(base, "daemon.pid")
				//: decimal content is the worst case, not an arbitrary one:
				//: readFence ACCEPTS it as a counter, so the attacker chooses
				//: the fencing token the victim hands to the protected
				//: resource — measured at 48213 -> 48214 against a pidfile,
				//: which is exactly this shape.
				if err := os.WriteFile(target, []byte("48213\n"), 0o600); err != nil {
					t.Fatalf("writing the target = %v", err)
				}
				symlinkOrSkip(t, target, lockPath)
				return func(t *testing.T) {
					t.Helper()
					content, err := os.ReadFile(target)
					if err != nil || string(content) != "48213\n" {
						t.Fatalf("the redirect target became %q (%v), want it untouched", content, err)
					}
				}
			},
			refuse: true,
		},
		{
			name: "symlink to a directory",
			plant: func(t *testing.T, base, lockPath string) func(t *testing.T) {
				t.Helper()
				target := filepath.Join(base, "adir")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatalf("creating the target directory = %v", err)
				}
				symlinkOrSkip(t, target, lockPath)
				//: refused by O_NOFOLLOW rather than by the directory-ness.
				//: Without the flag the open fails with EISDIR, which reports
				//: a medium fault for a deliberate substitution and tells an
				//: operator to retry.
				return func(t *testing.T) {
					t.Helper()
					entries, err := os.ReadDir(target)
					if err != nil || len(entries) != 0 {
						t.Fatalf("the redirect target gained %d entries (%v)", len(entries), err)
					}
				}
			},
			refuse: true,
		},
		{
			name: "hard link to another file",
			plant: func(t *testing.T, base, lockPath string) func(t *testing.T) {
				t.Helper()
				target := filepath.Join(base, "hard.lock")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatalf("writing the hard-link target = %v", err)
				}
				if err := os.Link(target, lockPath); err != nil {
					t.Skipf("this filesystem refuses hard links: %v", err)
				}
				//: accepted, and ADR 0081 says why it is not the same gap: a
				//: hard link needs the attacker to already have the target,
				//: which is most of what the redirection would have bought.
				return func(*testing.T) {}
			},
			refuse: false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		base := t.TempDir()
		dir := plantedDir(t)
		locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: dir})
		//: the directory rule accepted 0777|sticky, which is this test's
		//: premise rather than an aside.
		if err != nil {
			t.Fatalf("NewFileLocker on a 0777|sticky directory = %v, want a locker", err)
		}
		verify := c.plant(t, base, discoverLockPath(t, locker, dir))

		lease, held, acquireErr := locker.TryAcquire(t.Context(), victimName)
		//: the refusing half.
		if c.refuse {
			//: held and lease are asserted separately from the code because
			//: they fail apart: a locker returning a lease AND an error is a
			//: different bug from one refusing with the wrong sentinel, and
			//: held is what the caller actually branches on.
			if held || lease != nil {
				t.Fatalf("TryAcquire over a %s = (%v, %v), want no lease", c.name, lease, held)
			}
			//: LOCK_BACKEND_FAILED would tell an operator to retry, which is
			//: the single wrong response to a deliberate substitution.
			if !errs.HasCode(acquireErr, svclock.CodeLockPathRedirected) {
				t.Fatalf("TryAcquire over a %s = %v, want LOCK_PATH_REDIRECTED", c.name, acquireErr)
			}
			verify(t)
			//: verdict pinned.
			return
		}
		//: the accepting half: O_NOFOLLOW on a path that is not a symbolic
		//: link must change nothing at all.
		if acquireErr != nil || !held || lease == nil {
			t.Fatalf("TryAcquire over a %s = (%v, %v, %v), want a lease", c.name, lease, held, acquireErr)
		}
		//: and a working lease, not merely a non-nil one.
		if lease.Fence() == 0 {
			t.Fatalf("TryAcquire over a %s minted fence 0, want a counted token", c.name)
		}
		if releaseErr := lease.Release(t.Context()); releaseErr != nil {
			t.Fatalf("Release after a %s = %v", c.name, releaseErr)
		}
		verify(t)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTheOrdinaryPathIsUnchangedByTheNoFollowOpen walks the whole lifecycle on
// a lock file that is an ordinary file, because that is the path every caller
// is on and the one a new open flag breaks silently.
//
// It is deliberately separate from the table above: that one proves the
// refusal, this one proves the absence of a cost. A regression here reads as
// "locking stopped working" rather than "the hardening is missing", and the
// two want different names in a failure report.
func TestTheOrdinaryPathIsUnchangedByTheNoFollowOpen(t *testing.T) {
	t.Parallel()
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: plantedDir(t)})
	if err != nil {
		t.Fatalf("NewFileLocker = %v", err)
	}
	//: the first acquisition: the lock file does not exist yet, so this
	//: exercises O_CREATE next to the new flag.
	first, err := locker.Acquire(t.Context(), "ordinary")
	if err != nil {
		t.Fatalf("the first Acquire = %v", err)
	}
	//: contended: the answer is "held", and it is an answer rather than an
	//: error.
	contended, held, err := locker.TryAcquire(t.Context(), "ordinary")
	if err != nil || held || contended != nil {
		t.Fatalf("TryAcquire while held = (%v, %v, %v), want (nil, false, nil)", contended, held, err)
	}
	if releaseErr := first.Release(t.Context()); releaseErr != nil {
		t.Fatalf("Release = %v", releaseErr)
	}
	//: re-acquisition: the lock file now EXISTS as a regular file, which is
	//: the case O_NOFOLLOW must leave alone.
	second, err := locker.Acquire(t.Context(), "ordinary")
	if err != nil {
		t.Fatalf("the second Acquire = %v", err)
	}
	//: and the ledger kept counting across the two opens.
	if second.Fence() <= first.Fence() {
		t.Fatalf("the fence went %d -> %d, want strictly increasing", first.Fence(), second.Fence())
	}
	if releaseErr := second.Release(t.Context()); releaseErr != nil {
		t.Fatalf("the second Release = %v", releaseErr)
	}
}
