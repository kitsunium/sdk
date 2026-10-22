package consensus

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// treeOf is every file under dir, by path, with its bytes: what a reboot
// would find on the disk if nothing wrote after the stop.
func treeOf(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || e.Name() == fileLock || e.Name() == fileAudit {
			return err
		}
		b, rerr := os.ReadFile(p)
		out[p] = b
		return rerr
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sameTree fails when dir differs from before.
func sameTree(t *testing.T, dir string, before map[string][]byte, what string) {
	t.Helper()
	after := treeOf(t, dir)
	for p, b := range after {
		if old, ok := before[p]; !ok || !bytes.Equal(old, b) {
			t.Fatalf("%s wrote %s in the boot of a STORAGE_FAILED stop", what, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			t.Fatalf("%s removed %s in the boot of a STORAGE_FAILED stop", what, p)
		}
	}
}

// stoppedThisBoot leaves base's log with its last segment sealed — a
// rotation whose seal reached the page cache and whose next segment was
// never published — and a STORAGE_FAILED stop of this boot, its marker in
// both places, as a node whose seal datasync failed leaves it.
func stoppedThisBoot(t *testing.T) string {
	t.Helper()
	dir, d := base(t)
	l := d.d.log
	tail(t, d, appendRecord(nil, &record{kind: recSeal, end: true, batch: l.batch, index: uint64(l.off)}), 0)
	if dirErr, _ := l.markStopEach(coreconsensus.CodeStorageFailed, "fdatasync seal"); dirErr != nil {
		t.Fatal(dirErr)
	}
	l.Close()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Opening a data directory in the boot of a STORAGE_FAILED stop writes
// nothing: recovery used to publish the next segment past a seal read from
// the page cache before any marker was checked, and the reboot that clears
// the marker found a hole in a sealed segment (#563). The start is refused
// STOP_MARKER_PRESENT, Inspect still reports, and after the reboot the node
// opens and finishes the rotation.
func TestAnOpenInTheBootOfAStorageFailureWritesNothing(t *testing.T) {
	dir := stoppedThisBoot(t)
	before := treeOf(t, dir)
	d := openDir(t, dir, 1<<16)
	if err := d.d.log.checkMarkers(); !errors.Is(err, coreconsensus.StopMarkerPresent) {
		t.Fatalf("start check: %v", err)
	}
	r, err := Inspect(d)
	if err != nil {
		t.Fatal(err)
	}
	if !slicesContain(r.Markers, markerStopPrefix+"STORAGE_FAILED") {
		t.Fatalf("Inspect markers %v", r.Markers)
	}
	if _, _, err := Init(d, coreconsensus.ClusterID{1}, "s"); err == nil {
		t.Fatalf("Init in the boot of the stop: %v", err)
	}
	if err := ClearQuarantine(d, TruncateAndStaySilent, "op:clear"); !errors.Is(err, coreconsensus.StopMarkerPresent) {
		t.Fatalf("TruncateAndStaySilent in the boot of the stop: %v", err)
	}
	d.d.log.Close()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	sameTree(t, dir, before, "an open")
	if _, err := os.Stat(filepath.Join(dir, "log", segName(2))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the next segment was published past an unsynced seal")
	}
	reboot(t, dir)
	d = openDir(t, dir, 1<<16)
	defer d.Close()
	if err := d.d.log.checkMarkers(); err != nil {
		t.Fatalf("start after the reboot: %v %v", err, errs.FieldsOf(err))
	}
	if _, la := d.Log().Bounds(); la != 3 {
		t.Fatalf("last %d after the reboot, want 3", la)
	}
}

// Recover in the boot of a STORAGE_FAILED stop is refused before it writes:
// it adopted the batch the page cache showed and wrote the forced entry
// after it, which the reboot quarantined (#563).
func TestRecoverInTheBootOfAStorageFailureIsRefused(t *testing.T) {
	c, nodes := threeOnDisk(t, 5)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	if dirErr, _ := keep.dd.d.log.markStopEach(coreconsensus.CodeStorageFailed, "fdatasync"); dirErr != nil {
		t.Fatal(dirErr)
	}
	keep.dd.d.log.Close()
	if err := keep.dd.Close(); err != nil {
		t.Fatal(err)
	}
	before := treeOf(t, keep.dir)
	keep.dd = openDir(t, keep.dir, 16<<10)
	st := must(keep.dd.Log().Stamp())(t)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	next := coreconsensus.ClusterID{12}
	opt := RecoverOptions{Confirm: next.String(), Operator: "op:recover", WitnessKeys: []ed25519.PublicKey{pub}}
	for _, dn := range nodes[1:] {
		opt.Fenced = append(opt.Fenced, coreconsensus.FencedAttestationValue{
			Voter: dn.id, Method: "powered-off", Receipts: [][]byte{ed25519.Sign(priv, ReceiptMessage(c.cid, next, dn.id))},
		})
	}
	self := coreconsensus.MemberValue{ID: keep.id, Incarnation: st.Incarnation, Slot: st.Slot, SPKI: MemorySPKI(keep.id), Voter: true}
	if err := Recover(keep.dd, next, []coreconsensus.MemberValue{self}, &opt); !errors.Is(err, coreconsensus.StopMarkerPresent) {
		t.Fatalf("Recover in the boot of the stop: %v", err)
	}
	keep.dd.d.log.Close()
	if err := keep.dd.Close(); err != nil {
		t.Fatal(err)
	}
	sameTree(t, keep.dir, before, "Recover")
	reboot(t, keep.dir)
	keep.dd = openDir(t, keep.dir, 16<<10)
	recoverAlone(t, c, nodes, keep, next)
	keep.crash(t, c)
}

// reboot makes the STORAGE_FAILED markers of dir another boot's: the
// boot-scoped one is gone with the tmpfs, the data directory's names
// another boot id.
func reboot(t *testing.T, dir string) {
	t.Helper()
	l := &fileLog{dir: dir, runDir: testRuntimeDir()}
	if err := os.Remove(l.runtimeMarker("STORAGE_FAILED")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	p := filepath.Join(dir, markerStopPrefix+"STORAGE_FAILED")
	b := must(os.ReadFile(p))(t)
	b = []byte(strings.Replace(string(b), "boot="+bootID(), "boot=another-boot", 1))
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func slicesContain(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// testRuntimeDir is the boot-scoped directory the tests' data directories
// use.
func testRuntimeDir() string { return runtimeMarkerDir() }
