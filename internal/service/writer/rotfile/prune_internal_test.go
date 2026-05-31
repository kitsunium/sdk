package rotfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
