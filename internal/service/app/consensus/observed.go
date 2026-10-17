package consensus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// What a non-member observer needs from the engine: read the committed log
// back, read a saved snapshot without a node, and start a new history from
// a snapshot and the tail it observed after it. An observer is never a
// member: these read, or write an offline directory, and nothing else.

// ReadCommitted returns the committed entries [from, to], to clamped to
// this node's commit index, at most maxBytes of them but always one when
// any is due. from below the first entry the log keeps is COMPACTED: the
// reader starts again from a snapshot. It reads the log store concurrently
// with the writer, as every reader does, and never waits on the loop.
func (n *Node) ReadCommitted(ctx context.Context, from, to uint64, maxBytes int) ([]coreconsensus.EntryValue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n.stopped.Load() {
		return nil, n.stoppedErr()
	}
	if from == 0 {
		from = 1
	}
	to = min(to, n.Status().Commit)
	if from > to {
		return nil, nil
	}
	if first, _ := n.cfg.Log.Bounds(); from < first {
		return nil, origin(coreconsensus.Compacted, errs.String("from", strconv.FormatUint(from, 10)), errs.String("first", strconv.FormatUint(first, 10)))
	}
	return n.cfg.Log.Entries(from, to+1, maxBytes, nil)
}

// OpenSnapshot reads a saved snapshot's encoded stream, as SaveSnapshot
// wrote it, without a node: the reader returns the decoded stream, checks
// each chunk as it decodes it, bounds the total by opt.MaxDecoded, and
// answers SNAPSHOT_CORRUPT instead of io.EOF when the sizes or the SHA-256
// differ from opt.Expected's. opt.Codec is flate when nil.
func OpenSnapshot(r io.Reader, opt *RestoreOptions) (io.Reader, error) {
	if opt == nil || opt.MaxDecoded <= 0 {
		return nil, restoreRefused("size bounds")
	}
	codec, err := restoreCodec(opt)
	if err != nil {
		return nil, err
	}
	meta := opt.Expected
	return &snapshotReader{cr: newChunkReader(r, codec, max(meta.ChunkSize, 1), opt.MaxDecoded), meta: meta}, nil
}

// snapshotReader is OpenSnapshot's reader: a chunk reader whose end is
// checked against the metadata.
type snapshotReader struct {
	cr   *chunkReader
	meta coreconsensus.SnapshotMetaValue
}

func (s *snapshotReader) Read(p []byte) (int, error) {
	n, err := s.cr.Read(p)
	if !errors.Is(err, io.EOF) {
		return n, err
	}
	if s.cr.total != s.meta.DecodedSize || s.cr.encoded != s.meta.EncodedSize {
		return n, snapshotCorrupt("size")
	}
	var got [32]byte
	copy(got[:], s.cr.sum.Sum(nil))
	if got != s.meta.SHA256 {
		return n, snapshotCorrupt("sha256")
	}
	return n, io.EOF
}

// ImportObserved starts a new history on an empty, joining directory from
// a saved snapshot and the committed tail an observer read after it: the
// tail is checked against the snapshot (consecutive indexes from its
// position, terms that never decrease, each payload's BodySum and the
// ChainSum chained from the snapshot's ChainTip), replayed through m from
// the snapshot, and the state it reaches is restored as RestoreSnapshot
// does, its last entry the index the new history kept. An empty tail is
// RestoreSnapshot itself. opt.Expected is the snapshot's metadata.
//
// A tail that raises the cluster version, changes the timing contract or
// the no-space alarm is refused: the restored metadata could not carry it;
// import from a snapshot cut after it. Membership entries are checked and
// passed over: the new history has its own members.
func ImportObserved(d DataDir, snap io.Reader, tail []coreconsensus.EntryValue, m coreconsensus.StateMachine, newCluster coreconsensus.ClusterID, members []coreconsensus.MemberValue, opt *RestoreOptions) (coreconsensus.SnapshotMetaValue, error) {
	if opt == nil || m == nil {
		return coreconsensus.SnapshotMetaValue{}, restoreRefused("no options or no state machine")
	}
	if len(tail) == 0 {
		return RestoreSnapshot(d, snap, newCluster, members, opt)
	}
	exp := opt.Expected
	if err := checkObservedTail(&exp, tail); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	codec, err := restoreCodec(opt)
	if err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	r, err := OpenSnapshot(snap, opt)
	if err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	if err := m.Restore(r, exp); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	// Whatever the machine left unread is read here, to the digest.
	if _, err := io.Copy(io.Discard, r); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	for i := range tail {
		e := tail[i]
		if e.Kind != coreconsensus.EntryCommand {
			continue
		}
		e.Token = coreconsensus.TokenValue{Cluster: exp.Log.Cluster, Epoch: exp.Log.Epoch, Term: e.Position.Term, Index: e.Position.Index}
		m.Apply(e)
	}
	s, err := m.Snapshot()
	if err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	defer s.Release()
	var buf bytes.Buffer
	cw := newChunkWriter(&buf, codec, defaultChunkSize)
	if _, err := s.WriteTo(cw); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	if err := cw.Close(); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	last := tail[len(tail)-1]
	meta := exp
	meta.ID = fmt.Sprintf("%020d-%020d", last.Position.Index, last.Position.Term)
	meta.Position, meta.ChainTip = last.Position, last.ChainSum
	meta.Codec, meta.ChunkSize = string(codec.Algorithm()), defaultChunkSize
	meta.EncodedSize, meta.DecodedSize = cw.encoded, cw.decoded
	copy(meta.SHA256[:], cw.sum.Sum(nil))
	meta.Membership.Index = last.Position.Index
	o := *opt
	o.Expected = meta
	return RestoreSnapshot(d, bytes.NewReader(buf.Bytes()), newCluster, members, &o)
}

// checkObservedTail checks that a tail continues a snapshot, entry by entry.
func checkObservedTail(exp *coreconsensus.SnapshotMetaValue, tail []coreconsensus.EntryValue) error {
	prev, term, chain := exp.Position.Index, exp.Position.Term, exp.ChainTip
	for i := range tail {
		e := &tail[i]
		switch {
		case e.Position.Index != prev+1:
			return restoreRefused(fmt.Sprintf("the tail holds %d after %d", e.Position.Index, prev))
		case e.Position.Term < term:
			return restoreRefused(fmt.Sprintf("the term goes back at %d", e.Position.Index))
		case !e.Kind.Known():
			return restoreRefused(fmt.Sprintf("unknown entry kind at %d", e.Position.Index))
		case e.Kind == coreconsensus.EntryClusterVersion, e.Kind == coreconsensus.EntryTimingContract,
			e.Kind == coreconsensus.EntryNoSpaceRaise, e.Kind == coreconsensus.EntryNoSpaceClear:
			return restoreRefused(fmt.Sprintf("the tail changes the engine's state at %d; import from a later snapshot", e.Position.Index))
		case e.BodySum != wire.BodySum(e.Data):
			return restoreRefused(fmt.Sprintf("the payload of %d does not match its BodySum", e.Position.Index))
		case e.ChainSum != wire.ChainSum(chain, e):
			return restoreRefused(fmt.Sprintf("the chain breaks at %d", e.Position.Index))
		}
		prev, term, chain = e.Position.Index, e.Position.Term, e.ChainSum
	}
	return nil
}
