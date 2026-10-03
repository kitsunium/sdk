package rotfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakeClock is a manually set clock for deterministic age-pruning tests.
type fakeClock struct {
	now time.Time
}

// Now returns the fixed instant; set the now field to control the cutoff.
func (f *fakeClock) Now() time.Time {
	//: return the caller-controlled instant so pruning is deterministic.
	return f.now
}

// Since returns the elapsed duration between t and the fixed instant.
func (f *fakeClock) Since(t time.Time) time.Duration {
	//: mirror clock.System semantics for the rare caller that needs it.
	return f.now.Sub(t)
}

// newAgedSink builds an open *rotatingSink at path with the given age cap and
// fake clock, registering a cleanup that surfaces any close failure.
func newAgedSink(t *testing.T, path string, maxAgeDays int, now time.Time) *rotatingSink {
	t.Helper()
	sink, oerr := newRotatingSink(&Config{Path: path, MaxBytes: 8, MaxAgeDays: maxAgeDays, Clock: &fakeClock{now: now}})
	//: construction must succeed for the fixtures below.
	if oerr != nil {
		t.Fatalf("newRotatingSink: %v", oerr)
	}
	t.Cleanup(func() {
		//: surface a cleanup close failure rather than discarding it.
		if cerr := sink.Close(); cerr != nil {
			t.Errorf("cleanup close: %v", cerr)
		}
	})
	rs, ok := sink.(*rotatingSink)
	//: document the concrete return type the white-box test relies on.
	if !ok {
		t.Fatalf("sink is %T, want *rotatingSink", sink)
	}
	return rs
}

// seedBackup writes a rotated sibling at slot n with the given mtime.
func seedBackup(t *testing.T, path string, n int, mtime time.Time) string {
	t.Helper()
	name := path + "." + itoa(n)
	//: a small payload is enough; only the mtime drives age pruning.
	if werr := os.WriteFile(name, []byte("old\n"), 0o600); werr != nil {
		t.Fatalf("seed backup %d: %v", n, werr)
	}
	//: stamp the mtime so the cutoff comparison is deterministic.
	if cerr := os.Chtimes(name, mtime, mtime); cerr != nil {
		t.Fatalf("chtimes backup %d: %v", n, cerr)
	}
	return name
}

// itoa is a tiny local int-to-string so the tests avoid importing strconv for
// a single-digit slot index.
func itoa(n int) string {
	//: single-digit slots are all the fixtures need.
	return string(rune('0' + n))
}

// Test_rotatingSink_pruneByAge verifies aged siblings are removed and fresh
// ones survive, and that a non-positive cap is a no-op.
func Test_rotatingSink_pruneByAge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		maxAgeDays int
		ageDays    int
		wantGone   bool
	}{
		{name: "disabled keeps aged", maxAgeDays: 0, ageDays: 30, wantGone: false},
		{name: "older than cutoff pruned", maxAgeDays: 3, ageDays: 10, wantGone: true},
		{name: "younger than cutoff kept", maxAgeDays: 7, ageDays: 2, wantGone: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "app.log")
			now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
			rs := newAgedSink(t, path, tc.maxAgeDays, now)
			name := seedBackup(t, path, 1, now.AddDate(0, 0, -tc.ageDays))
			//: run the prune sweep under the configured policy.
			if perr := rs.pruneByAge(); perr != nil {
				t.Fatalf("pruneByAge: %v", perr)
			}
			_, serr := os.Lstat(name)
			gone := os.IsNotExist(serr)
			//: the survival of the seeded backup must match the expectation.
			if gone != tc.wantGone {
				t.Fatalf("backup gone = %v, want %v (stat err %v)", gone, tc.wantGone, serr)
			}
		})
	}
}

// Test_rotatingSink_pruneSlotByAge verifies the per-slot decision: a missing
// slot stops the sweep, a fresh slot survives, an aged slot is removed.
func Test_rotatingSink_pruneSlotByAge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		seed     bool
		ageDays  int
		wantMore bool
		wantGone bool
	}{
		{name: "missing slot stops sweep", seed: false, wantMore: false, wantGone: true},
		{name: "fresh slot kept", seed: true, ageDays: 1, wantMore: true, wantGone: false},
		{name: "aged slot pruned", seed: true, ageDays: 9, wantMore: true, wantGone: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "app.log")
			now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
			rs := newAgedSink(t, path, 5, now)
			name := path + ".1"
			//: only the seeded cases place a sibling on disk.
			if tc.seed {
				name = seedBackup(t, path, 1, now.AddDate(0, 0, -tc.ageDays))
			}
			cutoff := now.AddDate(0, 0, -5)
			more, perr := rs.pruneSlotByAge(1, cutoff)
			//: the slot decision must never error on these fixtures.
			if perr != nil {
				t.Fatalf("pruneSlotByAge: %v", perr)
			}
			//: the continue/stop signal must match the expectation.
			if more != tc.wantMore {
				t.Fatalf("more = %v, want %v", more, tc.wantMore)
			}
			_, serr := os.Lstat(name)
			gone := os.IsNotExist(serr)
			//: the slot's survival must match the expectation.
			if gone != tc.wantGone {
				t.Fatalf("slot gone = %v, want %v (stat err %v)", gone, tc.wantGone, serr)
			}
		})
	}
}

// Test_rotatingSink_pruneByAge_slotRemoveFails proves an EACCES on an aged slot
// aborts the sweep under the rotate sentinel rather than being swallowed.
func Test_rotatingSink_pruneByAge_slotRemoveFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"an unremovable aged backup fails the sweep"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: root bypasses the read-only directory bit, so the negative path is moot.
		if rootBypassesPerms() {
			return
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "app.log")
		now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
		//: a 3-day cap with a 30-day-old backup guarantees the slot is past cutoff.
		rs := newAgedSink(t, path, 3, now)
		seedBackup(t, path, 1, now.AddDate(0, 0, -30))
		//: freeze the directory so removing the aged .1 fails (restored in cleanup).
		freezeDirReadOnly(t, dir)
		//: the removal EACCES surfaces under the rotate sentinel.
		if perr := rs.pruneByAge(); !errs.HasCode(perr, CodeRotFileRotateFailed) {
			t.Errorf("pruneByAge err=%v want rotate-failed", perr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_rotatingSink_pruneSlotByAge_removeFails proves the per-slot decision
// returns (false, err) under the rotate sentinel when the aged slot is
// unremovable, rather than reporting "continue" with a swallowed error.
func Test_rotatingSink_pruneSlotByAge_removeFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"an unremovable aged slot returns false and the rotate sentinel"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: root bypasses the read-only directory bit, so the negative path is moot.
		if rootBypassesPerms() {
			return
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "app.log")
		now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
		rs := newAgedSink(t, path, 5, now)
		//: seed an aged .1 so a far-future cutoff classifies it as prunable.
		seedBackup(t, path, 1, now.AddDate(0, 0, -30))
		//: freeze the directory so removing the slot fails (restored in cleanup).
		freezeDirReadOnly(t, dir)
		//: a far-future cutoff forces the prune branch regardless of mtime.
		cutoff := now.AddDate(10, 0, 0)
		more, perr := rs.pruneSlotByAge(1, cutoff)
		//: a removal failure must stop the sweep and surface the rotate sentinel.
		if more || !errs.HasCode(perr, CodeRotFileRotateFailed) {
			t.Errorf("pruneSlotByAge more=%v err=%v want false+rotate-failed", more, perr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
