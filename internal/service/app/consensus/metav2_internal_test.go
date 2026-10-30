package consensus

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// metaV2 is m as the release that writes a checksummed metadata writes it:
// the fields of version 1 under the version byte 2, then the CRC-32C of
// every byte before it (#546).
func metaV2(m *coreconsensus.SnapshotMetaValue) []byte {
	b := wire.AppendSnapshotMeta(nil, m)
	b[0] = 2
	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli)))
}

// A node of this release restores the snapshots whose .meta a later release
// wrote in version 2, the one with a checksum: before, a version it did not
// know was no complete snapshot, every one was skipped, and a node rolled
// back over a compacted log started with no table. A bit that rots in such
// a .meta fails its sum, and the node falls back to an older snapshot with
// its log kept (#546 phase 1, ADR 0152 D18).
func TestASnapshotMetaOfVersion2IsRestored(t *testing.T) {
	dir := t.TempDir()
	snaps, err := newFileSnapshots(dir, storageFailed)
	if err != nil {
		t.Fatal(err)
	}
	c := &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{7, 2, 6}, size: 1, every: 20,
		configure: func(_ *testNode, cfg *Config) { cfg.Snapshots = snaps },
	}
	newClusterWith(t, c)
	tn := c.nodes[0]
	c.leader(5 * time.Second)
	var tok coreconsensus.TokenValue
	for i := range 70 {
		tok = propose(t, tn.node, "k"+u64s(uint64(i))+"=v")
	}
	c.waitApplied(tok.Index, 5*time.Second)
	cutsSettled(t, tn)
	c.kill(tn)
	if first, _ := tn.log.Bounds(); first <= 1 {
		t.Fatalf("setup: the log was never compacted (first %d)", first)
	}
	metas := must(snaps.List())(t)
	if len(metas) < 2 {
		t.Fatalf("setup: %d snapshots", len(metas))
	}
	for i := range metas {
		path := filepath.Join(dir, metas[i].ID+".meta")
		if err := os.WriteFile(path, metaV2(&metas[i]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Listed before the start: the started node may cut again as soon as
	// its election's entry reaches the next cut, and a listing right after
	// the start, on a slow runner, saw that fresh cut as the newest, the
	// oldest pruned (#759). Once started, the newest stays listed as it was
	// written.
	if got := must(snaps.List())(t); len(got) != len(metas) || got[0].ID != metas[0].ID {
		t.Fatalf("listed %d snapshots, newest %v; want %d, newest %s", len(got), got, len(metas), metas[0].ID)
	}
	c.start(tn)
	if holdsNoTable(t, tn) {
		t.Fatal("a node of this release skipped every version 2 metadata and started with no table")
	}
	if !listedAs(must(snaps.List())(t), &metas[0]) {
		t.Fatalf("the newest version 2 metadata %s is no longer listed once the node started", metas[0].ID)
	}
	if v, _ := tn.sm.get("k5"); v != "v" {
		t.Fatalf("the state was not restored: k5=%q", v)
	}

	// One bit of the newest .meta's chain tip rots, a field its id does not
	// name: its sum fails, it is no snapshot to restore from, and the node
	// keeps its log.
	cutsSettled(t, tn)
	c.kill(tn)
	metas = must(snaps.List())(t)
	newest := metas[0]
	rotted := newest
	rotted.ChainTip ^= 1 << 30
	enc, bad := metaV2(&newest), metaV2(&rotted)
	at := -1
	for i := range enc {
		if enc[i] != bad[i] {
			at = i
			break
		}
	}
	if at < 0 || bytes.Equal(enc[:len(enc)-4], bad[:len(bad)-4]) {
		t.Fatal("setup: the chain tip is not in the encoding")
	}
	enc[at] ^= 1 << 6 // bit 30 of the little-endian chain tip, under the sum
	if err := os.WriteFile(filepath.Join(dir, newest.ID+".meta"), enc, 0o600); err != nil {
		t.Fatal(err)
	}
	_, lastBefore := tn.log.Bounds()
	c.start(tn)
	for _, m := range must(snaps.List())(t) {
		// Restarted on the snapshot before it, the node replays its log to
		// the same commit and may cut again at the same index and term,
		// under the same id: a fresh meta written from its own state over
		// the rotted one, which #757 already allowed its sibling for
		// (#753, #759). Listed under that id, only the original's content
		// may stand, never the rotted chain tip.
		if m.ID == newest.ID && m.ChainTip != newest.ChainTip {
			t.Fatalf("a version 2 metadata whose sum fails is listed: chain tip %d (original %d, rotted %d)", m.ChainTip, newest.ChainTip, rotted.ChainTip)
		}
	}
	if _, last := tn.log.Bounds(); last < lastBefore {
		t.Fatalf("the log was cut to %d (held %d)", last, lastBefore)
	}
	if st := tn.node.Status(); st.Applied > lastBefore+1 || st.Commit > lastBefore+1 {
		t.Fatalf("the node reports past its log: %+v (log ends at %d)", st, lastBefore)
	}
}

// lastCut is the index of the last snapshot tn cut or started from, read
// under the lock its cuts write it under.
func lastCut(tn *testNode) uint64 {
	ss := &tn.node.snapState
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.last
}

// listedAs reports whether metas list m's id with m's chain tip: m as it
// was written, or a cut of the same index and term from the same log.
func listedAs(metas []coreconsensus.SnapshotMetaValue, m *coreconsensus.SnapshotMetaValue) bool {
	return slices.ContainsFunc(metas, func(x coreconsensus.SnapshotMetaValue) bool { return x.ID == m.ID && x.ChainTip == m.ChainTip })
}

// TestTheRestartedNodeCutsItsRottedNewestAgainFromItsOwnLog is the
// interleaving a slow runner gave TestASnapshotMetaOfVersion2IsRestored
// (#759, the sibling of #753): the newest snapshot sits at the log's last
// index, twenty entries past the one before it. Its version 2 metadata
// rots, and the restarted node, back on the snapshot before it, replays its
// log to the same commit and cuts again at the same index and term: a
// fresh metadata, under the rotted one's id, with the original's chain tip.
// A check of the id alone took it for the rotted metadata, which is never
// listed. Everything is in t.TempDir().
func TestTheRestartedNodeCutsItsRottedNewestAgainFromItsOwnLog(t *testing.T) {
	dir := t.TempDir()
	snaps, err := newFileSnapshots(dir, storageFailed)
	if err != nil {
		t.Fatal(err)
	}
	c := &cluster{
		t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{7, 5, 9}, size: 1, every: 20,
		configure: func(_ *testNode, cfg *Config) { cfg.Snapshots = snaps },
	}
	newClusterWith(t, c)
	tn := c.nodes[0]
	c.leader(5 * time.Second)
	// Thirty writes, then one at a time, each applied and its cut written
	// before the next: the next cut lands on the last index, twenty past
	// the one before.
	var metas []coreconsensus.SnapshotMetaValue
	for i := 0; ; i++ {
		if i == 90 {
			t.Fatalf("setup: the newest snapshot never sat at the last index, twenty past the one before: %v", metas)
		}
		tok := propose(t, tn.node, "k"+u64s(uint64(i))+"=v")
		if i < 30 {
			continue
		}
		c.waitApplied(tok.Index, 5*time.Second)
		cutsSettled(t, tn)
		metas = must(snaps.List())(t)
		if _, last := tn.log.Bounds(); len(metas) > 1 && metas[0].Position.Index == last && last == metas[1].Position.Index+20 {
			break
		}
	}
	c.kill(tn)
	newest := metas[0]
	rotted := newest
	rotted.ChainTip ^= 1 << 30
	for i := range metas {
		enc := metaV2(&metas[i])
		if i == 0 {
			// The chain tip rots under the sum, which then fails.
			bad := metaV2(&rotted)
			at := -1
			for j := range len(enc) - 4 {
				if enc[j] != bad[j] {
					at = j
					break
				}
			}
			if at < 0 {
				t.Fatal("setup: the chain tip is not in the encoding")
			}
			enc[at] ^= enc[at] ^ bad[at]
		}
		if err := os.WriteFile(filepath.Join(dir, metas[i].ID+".meta"), enc, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, lastBefore := tn.log.Bounds()
	c.start(tn)
	// The cut is waited for on the node, never by listing the store while
	// it writes: that cut renames its files over the rotted ones, and
	// Windows refuses the rename while a listing holds the .meta open, or
	// fails the listing that opens it mid-rename. Listed every millisecond,
	// the store had the node stop STORAGE_FAILED or the listing fail at
	// every run there (#768). It is read once the cut and its retention
	// are done.
	waitFor(t, "the cut of the restarted node under the rotted id", func() bool { return lastCut(tn) >= newest.Position.Index })
	cutsSettled(t, tn)
	if !listedAs(must(snaps.List())(t), &newest) {
		t.Fatalf("listed %v: want %s with the chain tip %d", must(snaps.List())(t), newest.ID, newest.ChainTip)
	}
	if _, last := tn.log.Bounds(); last < lastBefore {
		t.Fatalf("the log was cut to %d (held %d)", last, lastBefore)
	}
}
