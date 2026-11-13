package consensus

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
)

// listing is the names under dir, recursively, sorted.
func listing(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		names = append(names, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	slices.Sort(names)
	return names
}

// verdictOn reports whether vs names field.
func verdictOn(vs []PreflightVerdictValue, field string) bool {
	return slices.ContainsFunc(vs, func(v PreflightVerdictValue) bool { return v.Field == field })
}

// Preflight on a real disk measures 64 syncs, orders its percentiles, reads
// the free space the gauge reads, and leaves the directory as it found it
// (#1117).
func TestPreflightMeasuresTheDiskAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := listing(t, dir)
	v, err := Preflight(dir, PreflightOptions{Syncs: 64})
	if err != nil {
		t.Fatalf("Preflight: %v %v", err, errs.FieldsOf(err))
	}
	after := listing(t, dir)
	if !slices.Equal(before, after) {
		t.Fatalf("the directory held %v before and %v after", before, after)
	}
	// A sync faster than the clock's resolution reads 0 (Windows' clock
	// advances in steps, and a fast disk's median fell under one), so only
	// the slowest of the 64 must have measured something.
	if v.FsyncP50 < 0 || v.FsyncP50 > v.FsyncP99 || v.FsyncP99 > v.FsyncMax || v.FsyncMax <= 0 {
		t.Fatalf("p50 %v, p99 %v, max %v: want 0 <= p50 <= p99 <= max, max > 0", v.FsyncP50, v.FsyncP99, v.FsyncMax)
	}
	free := fsFree(dir)
	switch {
	case free < 0 && v.FreeBytes != -1:
		t.Fatalf("FreeBytes %d where fsFree is unknown, want -1", v.FreeBytes)
	case free >= 0:
		// Other writers share the filesystem: the two reads are a moment
		// apart, so a few MiB of drift is theirs, not Preflight's.
		const drift = 64 << 20
		if d := v.FreeBytes - free; d > drift || d < -drift {
			t.Fatalf("FreeBytes %d, fsFree %d: they differ by more than other writers explain", v.FreeBytes, free)
		}
	}
	if v.Flock != flock.Native || v.UserTimeout != unackedBound {
		t.Fatalf("Flock %v, UserTimeout %v; want the platform's %v, %v", v.Flock, v.UserTimeout, flock.Native, unackedBound)
	}
	if verdictOn(v.Verdicts, verdictElectionField) {
		t.Fatalf("a zero Timing judged nothing, got %v", v.Verdicts)
	}
}

// A timing contract whose ElectionTimeoutMin is not above three times the
// measured p99 is named, one above it is not; the syncs are a fake that
// moves a ManualClock (#1117).
func TestPreflightNamesAnElectionTimeoutUnderThreeSyncs(t *testing.T) {
	type tc struct {
		name    string
		min     time.Duration
		verdict bool
	}
	runCase := func(t *testing.T, c tc) {
		mc := clock.NewManualClock(time.Unix(0, 0))
		var n int
		// Syncs of 1..64 ms: p50 32 ms, p99 64 ms, max 64 ms.
		fake := func(*os.File) error {
			n++
			mc.Advance(time.Duration(n) * time.Millisecond)
			return nil
		}
		timing := TimingDefaults()
		timing.ElectionTimeoutMin = c.min
		v, err := preflight(t.TempDir(), &PreflightOptions{Syncs: 64, Timing: timing, Clock: mc}, fake)
		if err != nil {
			t.Fatalf("preflight: %v", err)
		}
		if v.FsyncP50 != 32*time.Millisecond || v.FsyncP99 != 64*time.Millisecond || v.FsyncMax != 64*time.Millisecond {
			t.Fatalf("p50 %v, p99 %v, max %v; want 32ms, 64ms, 64ms", v.FsyncP50, v.FsyncP99, v.FsyncMax)
		}
		got := slices.IndexFunc(v.Verdicts, func(vd PreflightVerdictValue) bool { return vd.Field == verdictElectionField })
		if (got >= 0) != c.verdict {
			t.Fatalf("ElectionTimeoutMin %v over p99 %v: verdicts %v, want one on %s: %v", c.min, v.FsyncP99, v.Verdicts, verdictElectionField, c.verdict)
		}
		if got >= 0 && v.Verdicts[got].Rule != verdictElectionRule {
			t.Fatalf("the verdict's rule is %q, want %q", v.Verdicts[got].Rule, verdictElectionRule)
		}
	}
	for _, c := range []tc{
		{name: "below", min: 150 * time.Millisecond, verdict: true},
		{name: "equal", min: 192 * time.Millisecond, verdict: true},
		{name: "above", min: 193 * time.Millisecond, verdict: false},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// Preflight on a directory a live DataDir holds is refused DATA_DIR_LOCKED
// and writes nothing; once it is closed, Preflight measures in its log
// directory and leaves it as it was. Where the platform has no flock(2),
// it reports Flock false instead of refusing (#1117).
func TestPreflightRefusesALockedDirectory(t *testing.T) {
	if !flock.Native {
		v, err := Preflight(t.TempDir(), PreflightOptions{Syncs: 4})
		if err != nil {
			t.Fatalf("Preflight on %s: %v, want a measurement", runtime.GOOS, err)
		}
		if v.Flock || !verdictOn(v.Verdicts, verdictFlockField) {
			t.Fatalf("on %s: Flock %v, verdicts %v; want false and a verdict on %s", runtime.GOOS, v.Flock, v.Verdicts, verdictFlockField)
		}
		t.Skipf("%s has no flock(2): no data directory can be held there (ADR 0144); the report is asserted above", runtime.GOOS)
	}
	dir := t.TempDir()
	d := openDir(t, dir, 1<<16)
	// The held log zero-fills its next segment in the background: the
	// listing is taken once that .tmp exists, or it lands between the two
	// listings and reads as the refused call's write (#1363, 17 times in 20
	// on tmpfs).
	settlePrepared(d)
	before := listing(t, dir)
	_, err := Preflight(dir, PreflightOptions{Syncs: 4})
	if !errors.Is(err, coreconsensus.DataDirLocked) {
		t.Fatalf("Preflight on a held directory = %v, want DATA_DIR_LOCKED", err)
	}
	if after := listing(t, dir); !slices.Equal(before, after) {
		t.Fatalf("a refused Preflight changed the directory: %v, then %v", before, after)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	var seen string
	spy := func(f *os.File) error {
		seen = filepath.Dir(f.Name())
		return datasync(f)
	}
	if _, err := preflight(dir, &PreflightOptions{Syncs: 4}, spy); err != nil {
		t.Fatalf("Preflight on a closed directory: %v", err)
	}
	if seen != filepath.Join(dir, "log") {
		t.Fatalf("the scratch file was in %s, want the log directory", seen)
	}
	if after := listing(t, dir); !slices.Equal(before, after) {
		t.Fatalf("Preflight changed the directory: %v, then %v", before, after)
	}
	// The lock was released: the directory opens again.
	openDir(t, dir, 1<<16).Close()
}

// Options out of their bounds and a directory that is not there are
// refused before anything is written; a failed sync is STORAGE_FAILED and
// still removes its scratch file (#1117).
func TestPreflightRefusesItsBoundsAndReportsAFailedSync(t *testing.T) {
	dir := t.TempDir()
	for _, o := range []PreflightOptions{
		{Syncs: -1},
		{Syncs: preflightMaxSyncs + 1},
		{RecordBytes: -1},
		{LogReserveBytes: -1},
		{Syncs: 2, RecordBytes: int(DefaultSegmentBytes)},
	} {
		if _, err := Preflight(dir, o); !errors.Is(err, coreconsensus.ConsensusMisconfigured) {
			t.Fatalf("Preflight(%+v) = %v, want CONSENSUS_MISCONFIGURED", o, err)
		}
	}
	if _, err := Preflight(filepath.Join(dir, "absent"), PreflightOptions{}); !errors.Is(err, coreconsensus.ConsensusMisconfigured) {
		t.Fatalf("Preflight on a missing directory = %v, want CONSENSUS_MISCONFIGURED", err)
	}
	failing := func(*os.File) error { return os.ErrClosed }
	if _, err := preflight(dir, &PreflightOptions{Syncs: 4}, failing); !errors.Is(err, coreconsensus.StorageFailed) {
		t.Fatalf("a failed sync = %v, want STORAGE_FAILED", err)
	}
	if got := listing(t, dir); !slices.Equal(got, []string{"."}) {
		t.Fatalf("a failed Preflight left %v behind", got)
	}
}

// The verdicts of the platform and of the free space are judged on what
// was measured, and none is named when nothing is broken.
func TestPreflightJudgesTheSpaceAndThePlatform(t *testing.T) {
	opts := PreflightOptions{LogReserveBytes: 100}
	clean := judgePreflight(&PreflightValue{FreeBytes: 100, Flock: true, UserTimeout: true}, &opts)
	if len(clean) != 0 {
		t.Fatalf("nothing broken, verdicts %v", clean)
	}
	all := judgePreflight(&PreflightValue{FreeBytes: 99}, &opts)
	for _, f := range []string{verdictReserveField, verdictFlockField, verdictUnackedField} {
		if !verdictOn(all, f) {
			t.Fatalf("verdicts %v name no %s", all, f)
		}
	}
	if verdictOn(judgePreflight(&PreflightValue{FreeBytes: -1, Flock: true, UserTimeout: true}, &opts), verdictReserveField) {
		t.Fatal("an unknown free space was judged under the reserve")
	}
}
