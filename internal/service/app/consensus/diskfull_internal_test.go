package consensus

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// errDiskFull stands for the ENOSPC a zero-fill meets on a full disk.
var errDiskFull = errors.New("no space left on device")

// fullDisk zero-fills half of the range, then fails as a full disk does:
// what a pre-fill leaves when the disk ran out under it.
func fullDisk(f *os.File, off, size int64) error {
	if _, err := f.WriteAt(make([]byte, (size-off)/2), off); err != nil {
		return err
	}
	return errDiskFull
}

// failingSnapKV is a kv machine whose every snapshot fails to be written,
// as one does on a full disk, counting the cuts started.
type failingSnapKV struct {
	*kv
	cuts *atomic.Int64
}

// failingSnap is a captured view whose write fails.
type failingSnap struct{}

func (failingSnap) WriteTo(io.Writer) (int64, error) { return 0, errDiskFull }
func (failingSnap) Release()                         {}

func (s failingSnapKV) Snapshot() (coreconsensus.Snapshot, error) {
	s.cuts.Add(1)
	return failingSnap{}, nil
}

// A cut that fails without stopping the node is not started again before
// its wait: every applied batch used to start a full cut on the apply
// goroutine, on a disk the last one had just filled (#520).
func TestAFailedCutWaitsBeforeTheNext(t *testing.T) {
	cuts := new(atomic.Int64)
	c := newClusterWith(t, &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{5, 2, 0}, every: 5, size: 1,
		configure: func(tn *testNode, cfg *Config) {
			cfg.Machine = failingSnapKV{kv: tn.sm, cuts: cuts}
			cfg.SnapshotPausesApply = true
		},
	})
	tn := c.nodes[0]
	c.leader(5 * time.Second)
	start := time.Now()
	for i := range 100 {
		propose(t, tn.node, fmt.Sprintf("k%d=%d", i, i))
	}
	// The waits double from cutRetryMin: over the run, at most one cut per
	// cutRetryMin elapsed, and the first.
	elapsed := time.Since(start)
	if got, most := cuts.Load(), 2+int64(elapsed/cutRetryMin); got > most {
		t.Fatalf("%d cuts started in %v, want at most %d", got, elapsed, most)
	}
	if tn.node.stopped.Load() {
		t.Fatal("a cut that failed without a storage failure stopped the node")
	}
}

// A segment whose zero-fill fails — in the background, or on the writer's
// path — leaves no .tmp behind: the partial file held every byte the fill
// wrote until the next rotation or start, on a disk the log shares with
// what the product stores (#520).
func TestAFailedZeroFillLeavesNoPartialSegment(t *testing.T) {
	dir := t.TempDir()
	d := openDir(t, dir, 4096)
	defer func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	}()
	l := d.d.log
	l.fill = fullDisk
	hs := coreconsensus.HardState{Term: 1}
	var err error
	for idx := uint64(1); idx < 2000 && err == nil; idx += 10 {
		if err = l.Append(ents(idx, idx+9, 1), &hs); err == nil {
			err = l.Sync()
		}
	}
	if !errors.Is(err, coreconsensus.StorageFailed) {
		t.Fatalf("the log never failed a rotation on a full disk: %v", err)
	}
	if p := l.prep; p != nil {
		<-p.done
	}
	if tmps := must(filepath.Glob(filepath.Join(dir, "log", "*.tmp")))(t); len(tmps) != 0 {
		t.Fatalf("a failed zero-fill left %v", tmps)
	}
}
