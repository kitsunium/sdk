package consensus

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// #509 phase 2: Recover records in the new identity the index up to which
// the kept entries were committed in the old history (AdoptedFrom), once
// the history is at protocol version 2. A token of the predecessor at or
// below it is the committed entry every node of the old history holds:
// once its term can no longer be read it is still applied, where phase 1
// answered LINEAGE_MISMATCH for the whole kept range. One in the adopted
// range above it still is LINEAGE_MISMATCH.
func TestRecoverRecordsWhereTheAdoptedRangeStarts(t *testing.T) {
	c, nodes := threeOnDisk(t, 30)
	live := make([]*Node, 0, len(nodes))
	for _, dn := range nodes {
		live = append(live, dn.node)
	}
	raiseNodes(t, live)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	old := must(keep.dd.Log().Stamp())(t).Log
	last, commit := appendPastCommit(t, keep.dd, []string{"stale0=a", "stale1=b"}, -1)
	keptTerm := must(keep.dd.Log().Term(last))(t)
	committedTerm := must(keep.dd.Log().Term(commit))(t)
	recoverAlone(t, c, nodes, keep, coreconsensus.ClusterID{9})
	defer keep.crash(t, c)

	l := keep.node.Status().Log
	if l.AdoptedFrom != commit || l.KeptIndex != last {
		t.Fatalf("recovered identity %+v, want AdoptedFrom %d (the persisted commit) and KeptIndex %d", l, commit, last)
	}
	if b := must(os.ReadFile(filepath.Join(keep.dir, fileStamp)))(t); b[0] != stampVersion2 {
		t.Fatalf("the stamp recording AdoptedFrom is of version %d", b[0])
	}
	mine := propose(t, keep.node, "mine=1")
	for i := range 120 {
		propose(t, keep.node, fmt.Sprintf("fill%d=x", i))
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if first, _ := keep.dd.Log().Bounds(); first > mine.Index+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: the log was never compacted past the kept range")
		}
	}
	wait := func(tok coreconsensus.TokenValue) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return keep.node.WaitApplied(ctx, tok)
	}
	committed := coreconsensus.TokenValue{Cluster: old.Cluster, Epoch: old.Epoch, Term: committedTerm, Index: commit}
	if err := wait(committed); err != nil {
		t.Fatalf("WaitApplied(%+v), committed in the old history, once its term can no longer be read: %v", committed, err)
	}
	adopted := coreconsensus.TokenValue{Cluster: old.Cluster, Epoch: old.Epoch, Term: keptTerm, Index: last}
	if err := wait(adopted); !errs.HasCode(err, coreconsensus.CodeLineageMismatch) {
		t.Fatalf("WaitApplied(%+v), in the adopted range, once its term can no longer be read: %v, want LINEAGE_MISMATCH", adopted, err)
	}
}

// Below protocol version 2, Recover records no AdoptedFrom and writes the
// stamp a node of the previous release opens: nothing past the kept range
// is trusted by its index, as in phase 1.
func TestRecoverBelowProtocol2RecordsNoAdoptedRange(t *testing.T) {
	c, nodes := threeOnDisk(t, 10)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	appendPastCommit(t, keep.dd, []string{"stale0=a"}, -1)
	recoverAlone(t, c, nodes, keep, coreconsensus.ClusterID{9})
	defer keep.crash(t, c)
	if l := keep.node.Status().Log; l.AdoptedFrom != 0 {
		t.Fatalf("a history at protocol 1 recorded AdoptedFrom %d", l.AdoptedFrom)
	}
	if b := must(os.ReadFile(filepath.Join(keep.dir, fileStamp)))(t); b[0] != stampVersion {
		t.Fatalf("the stamp of a history at protocol 1 is of version %d", b[0])
	}
}

// A stamp is written in version 2 when, and only when, its identity
// records AdoptedFrom, and each version reads back as written; a version 2
// stamp that records none is not one this package writes (#509).
func TestTheStampVersionFollowsAdoptedFrom(t *testing.T) {
	s := coreconsensus.StampValue{
		Log: coreconsensus.LogIdentityValue{Cluster: coreconsensus.ClusterID{2}, Epoch: 1, Predecessor: coreconsensus.ClusterID{1}, PredecessorEpoch: 1, KeptIndex: 20},
		ID:  7, State: coreconsensus.StampMember, Slot: "r0",
	}
	for _, adopted := range []uint64{0, 17} {
		s.Log.AdoptedFrom = adopted
		b := encodeStamp(s)
		if want := map[bool]byte{false: stampVersion, true: stampVersion2}[adopted != 0]; b[0] != want {
			t.Fatalf("AdoptedFrom %d: stamp of version %d, want %d", adopted, b[0], want)
		}
		if got, ok := decodeStamp(b); !ok || got != s {
			t.Fatalf("AdoptedFrom %d: read back %+v %v", adopted, got, ok)
		}
	}
	s.Log.AdoptedFrom = 1
	b := encodeStamp(s)
	at := 1 + 16 + 8 + 16 + 8 + 8
	b[at] = 0
	binary.LittleEndian.PutUint32(b[len(b)-4:], crc32.Checksum(b[:len(b)-4], castagnoli))
	if _, ok := decodeStamp(b); ok {
		t.Fatal("a version 2 stamp recording no AdoptedFrom was read")
	}
}
