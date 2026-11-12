package consensus

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
)

// CONSENSUS_PREALLOCATE=fallocate runs the whole suite on fallocated
// segments (#922): the lane of testdata/dmcrypt/run.sh with its fourth
// argument, on ext4 and xfs, runs the file log's tests that way.
func init() {
	if os.Getenv("CONSENSUS_PREALLOCATE") == "fallocate" {
		preallocEverywhere = true
	}
}

// openPrealloc opens dir with the mode asked for, on segments of seg bytes.
func openPrealloc(t *testing.T, dir string, seg int64, mode Preallocate) DataDir {
	t.Helper()
	if !flock.Native {
		t.Skipf("%s has no flock(2), so a data directory is refused there (ADR 0144)", runtime.GOOS)
	}
	d, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: seg, Preallocate: mode, RuntimeDir: testRuntimeDir()})
	if err != nil {
		t.Fatalf("open: %v %v", err, errs.FieldsOf(err))
	}
	return d
}

// preallocWarns records what the file log logs through bindWarn, attributes
// included.
type preallocWarns struct{ lines []string }

func (w *preallocWarns) warn(msg string, attrs ...corelogger.AttrValue) {
	var b strings.Builder
	b.WriteString(msg)
	for _, a := range attrs {
		b.WriteString(" " + a.Key + "=" + a.Value.String())
	}
	w.lines = append(w.lines, b.String())
}

// A Preallocate that names no mode is refused before anything is written.
func TestAnUnknownPreallocateIsRefusedMisconfigured(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	_, err := NewDataDirWith(dir, DataDirOptions{Preallocate: PreallocateFallocate + 1, RuntimeDir: testRuntimeDir()})
	if !errs.HasCode(err, coreconsensus.CodeConsensusMisconfigured) {
		t.Fatalf("NewDataDirWith(Preallocate 2) = %v, want CONSENSUS_MISCONFIGURED", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a refused data directory was created: %v", err)
	}
}

// A filesystem that answers EOPNOTSUPP to fallocate opens the log in
// ZeroFill, never refuses it, and says so in one line, logged once
// through the log of the node it serves (#922).
func TestAFilesystemWithoutFallocateOpensTheLogInZeroFill(t *testing.T) {
	saved := probePreallocate
	probePreallocate = func(string) string { return "the filesystem answers EOPNOTSUPP" }
	t.Cleanup(func() { probePreallocate = saved })
	dir := t.TempDir()
	d := openPrealloc(t, dir, 64<<10, PreallocateFallocate)
	defer d.Close()
	l := d.d.log
	if l.prealloc != PreallocateZeroFill {
		t.Fatalf("mode %d, want PreallocateZeroFill", l.prealloc)
	}
	if err := l.Append(ents(1, 400, 1), nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	checkEntries(t, l, 1, 400, 1)
	var w preallocWarns
	l.bindWarn(w.warn)
	l.bindWarn(w.warn)
	if len(w.lines) != 1 || !strings.Contains(w.lines[0], "EOPNOTSUPP") {
		t.Fatalf("logged %q, want one line naming EOPNOTSUPP", w.lines)
	}
	// A log that zero-fills because it was asked to logs nothing.
	d2 := openPrealloc(t, t.TempDir(), 64<<10, PreallocateZeroFill)
	defer d2.Close()
	var none preallocWarns
	d2.d.log.bindWarn(none.warn)
	if len(none.lines) != 0 && !preallocEverywhere && !syncEverywhere {
		t.Fatalf("a ZeroFill log logged %q", none.lines)
	}
}

// Where fallocate works, a fallocated log holds and recovers what it was
// given as a zero-filled one does, on segments of their full size, and the
// two modes open each other's directory: an unwritten extent reads as the
// hole a zero-filled tail is. Off Linux the log opens in ZeroFill and the
// same holds.
func TestAFallocatedLogRecoversAsAZeroFilledOne(t *testing.T) {
	dir := t.TempDir()
	const seg = 16 << 10
	d := openPrealloc(t, dir, seg, PreallocateFallocate)
	l := d.d.log
	if runtime.GOOS == "linux" && l.prealloc != PreallocateFallocate && fallocateProbe(l.logDir()) == "" {
		t.Fatalf("mode %d where the probe succeeds", l.prealloc)
	}
	if runtime.GOOS != "linux" && l.prealloc != PreallocateZeroFill {
		t.Fatalf("mode %d off Linux, want PreallocateZeroFill", l.prealloc)
	}
	hs := coreconsensus.HardState{Term: 2, Vote: 1, Commit: 900}
	// Batches of 50 small entries, so each one fits a segment and the
	// segments keep their fixed size.
	for i := uint64(1); i <= 1200; i += 50 {
		term, h := uint64(1), (*coreconsensus.HardState)(nil)
		if i > 600 {
			term, h = 2, &hs
		}
		if err := l.Append(ents(i, i+49, term), h); err != nil {
			t.Fatal(err)
		}
		mustSync(t, l)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	segs := must(filepath.Glob(filepath.Join(dir, "log", "seg-????????????????")))(t)
	if len(segs) < 3 {
		t.Fatalf("%d segment(s), want a rotation or more", len(segs))
	}
	for _, s := range segs {
		if st := must(os.Stat(s))(t); st.Size() != seg {
			t.Fatalf("%s holds %d bytes, want %d", s, st.Size(), seg)
		}
	}
	for _, mode := range []Preallocate{PreallocateFallocate, PreallocateZeroFill} {
		d = openPrealloc(t, dir, seg, mode)
		if f, la := d.Log().Bounds(); f != 1 || la != 1200 {
			t.Fatalf("mode %d: bounds %d %d", mode, f, la)
		}
		checkEntries(t, d.Log(), 1, 600, 1)
		checkEntries(t, d.Log(), 601, 1200, 2)
		if got := must(d.Log().HardState())(t); got != hs {
			t.Fatalf("mode %d: hard state %+v", mode, got)
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// fallocateFill leaves the range read as zeros and the file at its full
// size, whatever bytes the range held: ZERO_RANGE zeroes them where the
// filesystem supports it, and they are written over where it does not.
func TestFallocateFillZeroesWhatTheRangeHeld(t *testing.T) {
	f := must(os.Create(filepath.Join(t.TempDir(), "seg.tmp")))(t)
	defer f.Close()
	if _, err := f.Write(bytes.Repeat([]byte{0xff}, 6000)); err != nil {
		t.Fatal(err)
	}
	if err := osDisk.fallocateFill(f, 100, 16<<10); err != nil {
		t.Fatal(err)
	}
	b := must(os.ReadFile(f.Name()))(t)
	if len(b) != 16<<10 {
		t.Fatalf("size %d, want %d", len(b), 16<<10)
	}
	if !bytes.Equal(b[:100], bytes.Repeat([]byte{0xff}, 100)) || !allZero(b[100:]) {
		t.Fatal("the range does not read as zeros, or the bytes before it moved")
	}
}
