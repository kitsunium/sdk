package consensus

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
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
	snaps, err := newFileSnapshots(dir)
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
	c.start(tn)
	if tn.node.r.NoTable() {
		t.Fatal("a node of this release skipped every version 2 metadata and started with no table")
	}
	if got := must(snaps.List())(t); len(got) != len(metas) || got[0].ID != metas[0].ID {
		t.Fatalf("listed %d snapshots, newest %v; want %d, newest %s", len(got), got, len(metas), metas[0].ID)
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
		if m.ID == newest.ID {
			t.Fatalf("a version 2 metadata whose sum fails is listed: chain tip %d", m.ChainTip)
		}
	}
	if _, last := tn.log.Bounds(); last < lastBefore {
		t.Fatalf("the log was cut to %d (held %d)", last, lastBefore)
	}
	if st := tn.node.Status(); st.Applied > lastBefore+1 || st.Commit > lastBefore+1 {
		t.Fatalf("the node reports past its log: %+v (log ends at %d)", st, lastBefore)
	}
}
