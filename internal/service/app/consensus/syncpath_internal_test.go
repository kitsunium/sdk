package consensus

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
)

// CONSENSUS_SYNC_PATH=rwfdsync runs the whole suite on writes flagged
// RWF_DSYNC (#923): the lane of testdata/dmcrypt/run.sh with its fifth
// argument runs the file log's tests, its fuzz seeds and its power cuts
// that way.
func init() {
	if os.Getenv("CONSENSUS_SYNC_PATH") == "rwfdsync" {
		syncEverywhere = true
	}
}

// openSyncPath opens dir with the sync path asked for, on segments of seg
// bytes.
func openSyncPath(t *testing.T, dir string, seg int64, mode SyncPath) DataDir {
	t.Helper()
	if !flock.Native {
		t.Skipf("%s has no flock(2), so a data directory is refused there (ADR 0144)", runtime.GOOS)
	}
	d, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: seg, SyncPath: mode, RuntimeDir: testRuntimeDir()})
	if err != nil {
		t.Fatalf("open: %v %v", err, errs.FieldsOf(err))
	}
	return d
}

// A SyncPath that names no mode is refused before anything is written.
func TestAnUnknownSyncPathIsRefusedMisconfigured(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	_, err := NewDataDirWith(dir, DataDirOptions{SyncPath: SyncRWFDsync + 1, RuntimeDir: testRuntimeDir()})
	if !errs.HasCode(err, coreconsensus.CodeConsensusMisconfigured) {
		t.Fatalf("NewDataDirWith(SyncPath 2) = %v, want CONSENSUS_MISCONFIGURED", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a refused data directory was created: %v", err)
	}
}

// A kernel without pwritev2, or without RWF_DSYNC, opens the log in
// SyncFdatasync, never refuses it, and says so in one line, logged once
// through the log of the node it serves (#923).
func TestAKernelWithoutRWFDsyncOpensTheLogInFdatasync(t *testing.T) {
	saved := probeSyncPath
	probeSyncPath = func(string) string { return "the kernel refuses pwritev2(2) with RWF_DSYNC" }
	t.Cleanup(func() { probeSyncPath = saved })
	d := openSyncPath(t, t.TempDir(), 64<<10, SyncRWFDsync)
	defer d.Close()
	l := d.d.log
	if l.syncPath != SyncFdatasync {
		t.Fatalf("mode %d, want SyncFdatasync", l.syncPath)
	}
	if err := l.Append(ents(1, 400, 1), nil); err != nil {
		t.Fatal(err)
	}
	mustSync(t, l)
	checkEntries(t, l, 1, 400, 1)
	var w preallocWarns
	l.bindWarn(w.warn)
	l.bindWarn(w.warn)
	if len(w.lines) != 1 || !strings.Contains(w.lines[0], "RWF_DSYNC") {
		t.Fatalf("logged %q, want one line naming RWF_DSYNC", w.lines)
	}
}

// Where RWF_DSYNC works, a log written with it holds and recovers what it
// was given as an fdatasynced one does, and the two modes open each
// other's directory: the bytes on disk are the same. Off Linux the log
// opens in SyncFdatasync and the same holds.
func TestAnRWFDsyncLogRecoversAsAnFdatasyncOne(t *testing.T) {
	dir := t.TempDir()
	const seg = 16 << 10
	d := openSyncPath(t, dir, seg, SyncRWFDsync)
	l := d.d.log
	if runtime.GOOS == "linux" && l.syncPath != SyncRWFDsync && rwfDsyncProbe(l.logDir()) == "" {
		t.Fatalf("mode %d where the probe succeeds", l.syncPath)
	}
	if runtime.GOOS != "linux" && l.syncPath != SyncFdatasync {
		t.Fatalf("mode %d off Linux, want SyncFdatasync", l.syncPath)
	}
	hs := coreconsensus.HardState{Term: 2, Vote: 1, Commit: 900}
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
	if left := must(filepath.Glob(filepath.Join(dir, "log", rwfDsyncProbeName)))(t); len(left) != 0 {
		t.Fatalf("the probe left %q", left)
	}
	for _, mode := range []SyncPath{SyncRWFDsync, SyncFdatasync} {
		d = openSyncPath(t, dir, seg, mode)
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

// writeDsync writes every byte at its offset and nothing else, or, off
// Linux, writes nothing and answers it cannot.
func TestWriteDsyncWritesEveryByteAtItsOffset(t *testing.T) {
	f := must(os.Create(filepath.Join(t.TempDir(), "seg")))(t)
	defer f.Close()
	if err := osDisk.zeroFrom(f, 0, 8<<10); err != nil {
		t.Fatal(err)
	}
	b := bytes.Repeat([]byte{0xa5}, 5000)
	ok, err := writeDsync(f, b, 1000)
	if err != nil {
		t.Fatal(err)
	}
	got := must(os.ReadFile(f.Name()))(t)
	if !ok {
		if runtime.GOOS == "linux" && rwfDsyncProbe(filepath.Dir(f.Name())) == "" {
			t.Fatal("writeDsync refused where the probe succeeds")
		}
		if !allZero(got) {
			t.Fatal("a refused writeDsync wrote")
		}
		return
	}
	if len(got) != 8<<10 || !allZero(got[:1000]) || !bytes.Equal(got[1000:6000], b) || !allZero(got[6000:]) {
		t.Fatal("the bytes are not where writeDsync was asked to write them")
	}
}
