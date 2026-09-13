//go:build !windows

package lock_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// This file carries a Unix build constraint because the exposure it pins needs
// the lock file to be unlinkable WHILE it is held, and Windows refuses that at
// the operating-system level: os.OpenFile reaches CreateFileW without
// FILE_SHARE_DELETE. The Windows half asserts that refusal instead — see
// identity_windows_test.go — so neither platform is left without a gate.

// heldLease acquires name over dir, reusing the locker helper that skips where
// the platform has no file lock at all.
func heldLease(t *testing.T, dir, name string) corelock.Lease {
	t.Helper()
	lease, err := newFileLocker(t, dir).Acquire(t.Context(), name)
	if err != nil {
		t.Fatalf("Acquire(%s) = %v", name, err)
	}
	return lease
}

// TestExtendRefusesOnceTheLockFileIsNoLongerTheOneItsNameLeadsTo is the
// exposure, reduced to a table.
//
// Reproduced before the check existed, in a 0777|sticky directory — /tmp's
// mode, and a row checkDir accepts by name:
//
//	victime détient le verrou : fence=1 inode=69831
//	2e verrou APRÈS l'échange : held=true err=<nil> inode=69832
//	SPLIT : deux détenteurs, fences 1 et 1, inodes 69831 et 69832
//	Extend de la victime : <nil>
//
// The last line is what changes. The split is NOT prevented — the second
// holder still gets the lock, and TestTheSplitIsDetectedAndNotPrevented pins
// that as the guarantee rather than letting a reader assume otherwise.
func TestExtendRefusesOnceTheLockFileIsNoLongerTheOneItsNameLeadsTo(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		replace func(t *testing.T, path string)
		refuse  bool
	}
	tests := []tc{
		{
			name:    "untouched",
			replace: func(*testing.T, string) {},
			refuse:  false,
		},
		{
			name: "unlinked while held",
			replace: func(t *testing.T, path string) {
				if err := os.Remove(path); err != nil {
					t.Fatalf("unlinking the held lock file = %v", err)
				}
			},
			refuse: true,
		},
		{
			name: "unlinked and replaced by a fresh lock file",
			replace: func(t *testing.T, path string) {
				if err := os.Remove(path); err != nil {
					t.Fatalf("unlinking = %v", err)
				}
				if err := os.WriteFile(path, []byte("1\n"), 0o600); err != nil {
					t.Fatalf("planting a replacement = %v", err)
				}
			},
			refuse: true,
		},
		{
			name: "replaced by a symbolic link to a decoy",
			replace: func(t *testing.T, path string) {
				decoy := path + ".decoy"
				if err := os.WriteFile(decoy, []byte("1\n"), 0o600); err != nil {
					t.Fatalf("building the decoy = %v", err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatalf("unlinking = %v", err)
				}
				if err := os.Symlink(decoy, path); err != nil {
					t.Skipf("cannot create a symbolic link here: %v", err)
				}
			},
			refuse: true,
		},
		{
			name: "renamed away and replaced",
			replace: func(t *testing.T, path string) {
				if err := os.Rename(path, path+".moved"); err != nil {
					t.Fatalf("renaming = %v", err)
				}
				if err := os.WriteFile(path, []byte("7\n"), 0o600); err != nil {
					t.Fatalf("planting a replacement = %v", err)
				}
			},
			refuse: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		lease := heldLease(t, dir, "shared")
		releaseAtEnd(t, lease)
		c.replace(t, lockFilePath(dir, "shared"))
		err := lease.Extend(t.Context())
		//: the refusing half: the holder is inside a section it no longer
		//: owns, and the only thing the SDK can still do is say so.
		if c.refuse {
			if !errs.HasCode(err, svclock.CodeLockFileReplaced) {
				t.Fatalf("Extend after %q = %v, want LOCK_FILE_REPLACED", c.name, err)
			}
			//: verdict pinned.
			return
		}
		//: the accepting half, which is the row a check that is too eager
		//: loses: an ordinary held lease renews, every time, forever.
		if err != nil {
			t.Fatalf("Extend on an untouched held lease = %v, want nil", err)
		}
		//: twice, because a check that consumed something on its first call
		//: would pass the assertion above and fail a keepalive's second tick.
		if second := lease.Extend(t.Context()); second != nil {
			t.Fatalf("the second Extend on an untouched held lease = %v, want nil", second)
		}
	}
	//: one subtest per row, each on its own directory.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTheSplitIsDetectedAndNotPrevented states the guarantee as exactly what
// it is.
//
// An unlink-and-replace is not preventable: the entry is removed after the
// open, by an account the directory's permissions genuinely allow to remove
// it, and the victim's descriptor keeps working because a descriptor outlives
// its name. So a SECOND holder really does get the lock. What ships is that
// the first one finds out — and a test that only asserted the refusal would
// let a reader believe the exclusion had been restored.
func TestTheSplitIsDetectedAndNotPrevented(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	victim := heldLease(t, dir, "shared")
	releaseAtEnd(t, victim)

	//: a second locker, because two lockers over one directory have two
	//: in-process gates and only the flock can exclude them.
	second, err := svclock.NewFileLocker(svclock.FileConfig{Dir: dir})
	if errors.Is(err, coreproc.UnsupportedPlatform) {
		t.Skip("no native file lock on this platform")
	}
	if err != nil {
		t.Fatalf("the second NewFileLocker = %v", err)
	}
	//: before the swap the flock excludes it, which is the whole domain
	//: working — asserted so the row below means something.
	if _, held, tryErr := second.TryAcquire(t.Context(), "shared"); held || tryErr != nil {
		t.Fatalf("TryAcquire before the swap = (held=%v, %v), want (false, nil)", held, tryErr)
	}
	if removeErr := os.Remove(lockFilePath(dir, "shared")); removeErr != nil {
		t.Fatalf("unlinking the held lock file = %v", removeErr)
	}
	taken, held, tryErr := second.TryAcquire(t.Context(), "shared")
	//: NOT prevented, and that is the documented guarantee.
	if !held || tryErr != nil {
		t.Fatalf("TryAcquire after the swap = (held=%v, %v); this test encodes that the split is NOT prevented", held, tryErr)
	}
	releaseAtEnd(t, taken)
	//: and the fence went BACKWARDS, because the new inode's ledger starts
	//: again — the sharpest consequence, and the reason detection matters.
	if taken.Fence() > victim.Fence() {
		t.Fatalf("the new holder's fence = %d, which is ahead of the victim's %d — the ledger survived the swap and this test is asserting the wrong thing", taken.Fence(), victim.Fence())
	}
	//: detected.
	if !errs.HasCode(victim.Extend(t.Context()), svclock.CodeLockFileReplaced) {
		t.Fatalf("the victim's Extend after the swap = %v, want LOCK_FILE_REPLACED", victim.Extend(t.Context()))
	}
}

// TestAKeepaliveCancelsTheProtectedWorkWhenTheLockFileIsReplaced pins the
// reason Extend is where the check lives: it is the only call a holder makes
// during the section, so it is the only one that can reach work already in
// progress.
func TestAKeepaliveCancelsTheProtectedWorkWhenTheLockFileIsReplaced(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lease := heldLease(t, dir, "shared")
	releaseAtEnd(t, lease)

	if err := os.Remove(lockFilePath(dir, "shared")); err != nil {
		t.Fatalf("unlinking the held lock file = %v", err)
	}
	manual := clock.NewManualClock(time.Unix(0, 0))
	guarded, stop, err := svclock.Keepalive(t.Context(), lease, svclock.KeepaliveConfig{
		Every: renewEvery,
		Clock: manual,
	})
	if err != nil {
		t.Fatalf("Keepalive = %v", err)
	}
	defer stop()
	//: the ticker is armed before it is advanced, so nothing here waits on the
	//: wall clock (the ADR 0041 audit this package carries).
	manual.BlockUntil(1)
	manual.Advance(renewEvery)
	<-guarded.Done()
	cause := context.Cause(guarded)
	//: origin wins (ADR 0005): the cause IS the replaced file, and the
	//: keepalive's own code rides the wrap trail, so both are recoverable.
	if !errs.HasCode(cause, svclock.CodeLockFileReplaced) {
		t.Fatalf("context.Cause = %v, want LOCK_FILE_REPLACED", cause)
	}
	if !errs.HasCode(cause, svclock.CodeLockKeepaliveLost) {
		t.Fatalf("context.Cause = %v, want the keepalive code on the wrap trail too", cause)
	}
}
