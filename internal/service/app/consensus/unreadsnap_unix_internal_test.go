//go:build !windows

package consensus

import (
	"os"
	"path/filepath"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// #789: a survivor whose newest snapshot is an install a crash interrupted
// (published past the end of its log, #506) cannot read that snapshot while
// Recover runs: its .snap cannot be opened, a stand-in for a read error of
// the moment (EIO, EMFILE, EACCES). interruptedInstall set it aside as one
// that fails its checksums, Recover planned from the short log and wrote
// its forced entry there, and a start that read the snapshot again restored
// it (a STORAGE_FAILED is no fault of its own, the start keeps it) and
// dropped the log: the forced entry went, and the node ran the snapshot's
// old voters under the new cluster. Recover and Inspect now refuse,
// STORAGE_FAILED, the directory unchanged; once the snapshot reads, Recover
// finishes the install and the node runs the forced configuration. A .meta
// Open cannot read is the disk's failure too, not a missing one. Every path
// is the test's own, under t.TempDir().
func TestRecoverRefusesAnInterruptedInstallItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	c, nodes, keep := installInterrupted(t)
	snap := must(keep.dd.Snapshots().List())(t)[0]
	snapPath := filepath.Join(keep.dir, "snap", snap.ID+".snap")
	metaPath := filepath.Join(keep.dir, "snap", snap.ID+".meta")
	t.Cleanup(func() {
		_ = os.Chmod(snapPath, 0o600)
		_ = os.Chmod(metaPath, 0o600)
	})
	first, last := keep.dd.Log().Bounds()
	stamp := must(keep.dd.Log().Stamp())(t)
	next := coreconsensus.ClusterID{7, 5, 9}
	members, opt := recoverArgs(t, c, nodes, keep, next)

	if err := os.Chmod(snapPath, 0); err != nil {
		t.Fatal(err)
	}
	if err := Recover(keep.dd, next, members, opt); !errs.HasCode(err, coreconsensus.CodeStorageFailed) {
		f, l := keep.dd.Log().Bounds()
		t.Fatalf("Recover over the install at %d it cannot read = %v, want STORAGE_FAILED (log now [%d,%d])", snap.Position.Index, err, f, l)
	}
	if _, err := Inspect(keep.dd); !errs.HasCode(err, coreconsensus.CodeStorageFailed) {
		t.Fatalf("Inspect over the unreadable install = %v, want STORAGE_FAILED", err)
	}
	if f, l := keep.dd.Log().Bounds(); f != first || l != last {
		t.Fatalf("the refused Recover moved the log from [%d,%d] to [%d,%d]", first, last, f, l)
	}
	if st := must(keep.dd.Log().Stamp())(t); st.Log.Cluster != stamp.Log.Cluster {
		t.Fatalf("the refused Recover stamped cluster %s", st.Log.Cluster)
	}
	if markers := must(stopMarkers(keep.dir))(t); len(markers) != 0 {
		t.Fatalf("the refused Recover left %v", markers)
	}
	if err := os.Chmod(snapPath, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(metaPath, 0); err != nil {
		t.Fatal(err)
	}
	if h, err := keep.dd.Snapshots().Open(snap.ID); !errs.HasCode(err, coreconsensus.CodeStorageFailed) {
		if err == nil {
			_ = h.Close()
		}
		t.Fatalf("Open of a .meta it cannot read = %v, want STORAGE_FAILED", err)
	}
	if err := os.Chmod(metaPath, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Recover(keep.dd, next, members, opt); err != nil {
		t.Fatalf("Recover once the install reads: %v", err)
	}
	c.cid = next
	keep.sm = newKV()
	cfg := diskConfig(c, keep)
	cfg.MinVoters = 1
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatalf("the start after Recover is refused: %v", err)
	}
	keep.node = n
	defer keep.crash(t, c)
	if m := n.Membership(); len(m.Members) != 1 || m.Members[0].ID != keep.id {
		f, l := keep.dd.Log().Bounds()
		t.Fatalf("the recovered node runs %d members, not the forced configuration (log [%d,%d])", len(m.Members), f, l)
	}
}
