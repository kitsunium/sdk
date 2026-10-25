package consensus

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// #665: a node of protocol 2 cannot decode the snapshot metadata of version
// 3 that #659 writes once a cluster is at protocol 3. Its transport refused
// such an offer before the node judged it, and its start skipped such a
// .meta as one that rotted, so the stop of #513 and #640, which runs only
// on a metadata that decodes, never ran: the node left replication without
// a word and the leader offered it the snapshot again at every probe. That
// release is shipped; this build is the old reader of the next protocol,
// and meets a metadata version it does not know, 4, as that release meets
// 3. These tests are the issue's, run against this build at version 4.

// metaOfUnknownVersion is m in a metadata version this build does not know,
// 4: version 3's encoding with the version byte 4 and its CRC-32C computed
// again, the shape every version from 2 keeps (D18). It is what a build
// past ProtocolMax may write, as version 3 is to the protocol 2 release.
func metaOfUnknownVersion(m *coreconsensus.SnapshotMetaValue) []byte {
	b := wire.AppendSnapshotMeta(nil, m)
	b[0] = 4
	end := len(b) - 4
	binary.LittleEndian.PutUint32(b[end:], wire.BodySum(b[:end]))
	return b
}

// rawOffer sends, from leader l's transport to f's snapshot lane, one offer
// at term whose metadata is the encoded meta, and returns f's accept, or the
// error reading it.
func rawOffer(t *testing.T, l, f *tcpNode, term uint64, meta []byte) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := l.tr.dial(ctx, f.addr, laneSnapshot, &f.spki)
	if err != nil {
		t.Fatalf("setup: dial %d's snapshot lane: %v", f.id, err)
	}
	defer func() { _ = c.Close() }()
	body := binary.LittleEndian.AppendUint64(nil, term)
	body = append(body, meta...)
	if err := writeChunk(c, body); err != nil {
		t.Fatalf("setup: write the offer: %v", err)
	}
	return readAccept(c, 3*time.Second)
}

// acceptCode is the refusal an accept carries, 0 for none or no accept.
func acceptCode(acc []byte) errs.Code {
	if len(acc) != 13 {
		return 0
	}
	return errs.Code(binary.LittleEndian.Uint32(acc[8:]))
}

// A follower offered, by its own leader in its term, a snapshot whose
// metadata is of a version it does not know stops VERSION_UNSUPPORTED, as
// #513 and #640 stop it on one that decodes. The same offer at an earlier
// term, a stale one, names no leader of it: it is refused NOT_LEADER and
// stops nothing.
func TestAnOfferOfAMetadataVersionThisBuildCannotReadStopsTheNode(t *testing.T) {
	_, _, ns := newTCPCluster(t)
	l := tcpLeader(t, ns)
	var f *tcpNode
	for _, tn := range ns {
		if tn != l {
			f = tn
			break
		}
	}
	waitFor(t, "the follower knows the leader", func() bool { return f.node.Status().Leader == l.id })
	st := f.node.Status()
	idx := st.Commit + 100
	meta := coreconsensus.SnapshotMetaValue{
		ID: fmt.Sprintf("%020d-%020d", idx, st.Term), Version: 1, Log: f.node.lineage(),
		Position: coreconsensus.PositionValue{Term: st.Term, Index: idx}, Membership: f.node.Membership(),
		ClusterVersion: raft.ProtocolMax + 1, ClusterGeneration: 3, Codec: "flate", ChunkSize: 1 << 16, EncodedSize: 1, DecodedSize: 1,
	}
	enc := metaOfUnknownVersion(&meta)
	if err := wire.ParseSnapshotMeta(enc, new(coreconsensus.SnapshotMetaValue)); !errors.Is(err, coreconsensus.VersionUnsupported) {
		t.Fatalf("setup: this build parses the version 4 metadata: %v", err)
	}

	// A term below the follower's is never its leader's now, whatever
	// elections the load brings meanwhile.
	acc, err := rawOffer(t, l, f, st.Term-1, enc)
	time.Sleep(100 * time.Millisecond)
	if r, stopped := f.node.StopReason(); stopped {
		t.Fatalf("an offer at a term the follower is not in stopped it: %v", r.Err)
	}
	t.Logf("version 4 metadata at an earlier term: accept refused %v (%v)", acceptCode(acc), err)
	if acceptCode(acc) != coreconsensus.CodeNotLeader {
		t.Errorf("an offer at an earlier term was answered %v, want NOT_LEADER", acceptCode(acc))
	}

	// The follower's term is read again just before the offer, l still its
	// leader: an election under load since would make st.Term stale.
	var term uint64
	waitFor(t, "the follower follows l", func() bool {
		s := f.node.Status()
		term = s.Term
		return s.Leader == l.id && l.node.Status().Term == term
	})
	acc, err = rawOffer(t, l, f, term, enc)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, stopped := f.node.StopReason(); stopped {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	r, stopped := f.node.StopReason()
	t.Logf("version 4 metadata from the leader: accept refused %v (%v), follower stopped=%v %v", acceptCode(acc), err, stopped, r.Err)
	if !stopped {
		t.Fatalf("a follower offered by its leader a metadata of a version it does not know never stopped: its transport refuses the offer before its node judges it, and the leader offers it again at every probe")
	}
	if !errors.Is(r.Err, coreconsensus.VersionUnsupported) {
		t.Fatalf("the follower stopped with %v, want VERSION_UNSUPPORTED", r.Err)
	}
	if acceptCode(acc) != coreconsensus.CodeVersionUnsupported {
		t.Errorf("the leader was answered %v, want VERSION_UNSUPPORTED: it logs that its peer stops", acceptCode(acc))
	}
}

// writeSnapshotFiles writes, in the snapshot directory dir, a .snap and the
// .meta meta of m.
func writeSnapshotFiles(t *testing.T, dir string, m *coreconsensus.SnapshotMetaValue, meta []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, m.ID+".snap"), []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, m.ID+".meta"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A .meta of a version this build does not know, whose CRC-32C holds, is
// what a newer build wrote: the snapshot store refuses to open over it
// VERSION_UNSUPPORTED, and so does the data directory, before anything in
// it is removed, and a listing reports it rather than skip it. Skipped, a
// node rolled back past the raise saw none of the snapshots it held. One
// whose sum fails rotted, and is skipped as before (#546).
func TestAMetaOfAVersionThisBuildCannotReadRefusesTheOpen(t *testing.T) {
	m := coreconsensus.SnapshotMetaValue{
		ID: fmt.Sprintf("%020d-%020d", 500, 3), Version: 1,
		Position: coreconsensus.PositionValue{Term: 3, Index: 500}, ClusterVersion: raft.ProtocolMax + 1, ClusterGeneration: 3,
		Codec: "flate", ChunkSize: 1 << 16, EncodedSize: 1, DecodedSize: 1,
	}
	newer := metaOfUnknownVersion(&m)

	t.Run("store", func(t *testing.T) {
		dir := t.TempDir()
		writeSnapshotFiles(t, dir, &m, newer)
		// What an open sweeps: a .snap without its .meta, and every receive
		// spool but the newest (#527).
		orphan := fmt.Sprintf("%020d-%020d.snap", 400, 3)
		older, newest := fmt.Sprintf("%020d-%020d.spool", 100, 2), fmt.Sprintf("%020d-%020d.spool", 200, 2)
		for _, name := range []string{orphan, older, newest} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte{0}, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		s, err := newFileSnapshots(dir, storageFailed)
		if err == nil {
			got, lerr := s.List()
			t.Fatalf("the store opened over a version 4 .meta: List() = %d snapshots, err %v", len(got), lerr)
		}
		if !errors.Is(err, coreconsensus.VersionUnsupported) {
			t.Fatalf("open over a version 4 .meta: %v, want VERSION_UNSUPPORTED", err)
		}
		for _, name := range []string{m.ID + ".snap", m.ID + ".meta", orphan, older, newest} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Errorf("the refused open removed %s: %v", name, err)
			}
		}
		// Control: without the newer .meta, the open sweeps what the
		// refused one left.
		if err := os.Remove(filepath.Join(dir, m.ID+".meta")); err != nil {
			t.Fatal(err)
		}
		if _, err := newFileSnapshots(dir, storageFailed); err != nil {
			t.Fatalf("control: the open without the newer .meta: %v", err)
		}
		for _, name := range []string{orphan, older} {
			if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("control: the open kept %s (%v), so the check above proves nothing", name, err)
			}
		}
	})
	t.Run("data directory", func(t *testing.T) {
		// The refusal does not depend on the segment's size, and the
		// reopen scans the unused tail of the active segment for records
		// past a torn one: at the default 64 MiB that scan took the whole
		// subtest, 25 s under -race with coverage, and alone pushed a
		// shard of consensus_test past Bazel's 60 s.
		const seg = 1 << 16
		dir := t.TempDir()
		d := openDir(t, dir, seg)
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		writeSnapshotFiles(t, filepath.Join(dir, "snap"), &m, newer)
		d, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: seg, RuntimeDir: testRuntimeDir()})
		if err == nil {
			_ = d.Close()
			t.Fatal("the data directory opened over a version 4 .meta")
		}
		if !errors.Is(err, coreconsensus.VersionUnsupported) {
			t.Fatalf("data directory over a version 4 .meta: %v, want VERSION_UNSUPPORTED", err)
		}
	})
	t.Run("listing", func(t *testing.T) {
		dir := t.TempDir()
		s, err := newFileSnapshots(dir, storageFailed)
		if err != nil {
			t.Fatal(err)
		}
		writeSnapshotFiles(t, dir, &m, newer)
		got, err := s.List()
		if !errors.Is(err, coreconsensus.VersionUnsupported) {
			t.Fatalf("a version 4 .meta listed: %d snapshots, err %v, want VERSION_UNSUPPORTED", len(got), err)
		}
	})
	t.Run("rotted", func(t *testing.T) {
		dir := t.TempDir()
		rotted := wire.AppendSnapshotMeta(nil, &m)
		rotted[0] = 4 // the sum, over version 3's bytes, now fails
		writeSnapshotFiles(t, dir, &m, rotted)
		s, err := newFileSnapshots(dir, storageFailed)
		if err != nil {
			t.Fatalf("a .meta whose version byte rotted refused the open: %v", err)
		}
		if got, err := s.List(); err != nil || len(got) != 0 {
			t.Fatalf("a .meta whose version byte rotted: List() = %d snapshots, err %v, want it skipped", len(got), err)
		}
	})
}
