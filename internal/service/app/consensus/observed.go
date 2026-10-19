package consensus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// What a non-member observer needs from the engine: read the committed log
// back, read a saved snapshot without a node, and start a new history from
// a snapshot and the tail it observed after it. An observer is never a
// member: these read, or write an offline directory, and nothing else.

// ReadCommitted returns the committed entries [from, to], to clamped to
// what this node's log store holds as committed (its own commit, or the
// last entry applied from it), never to the commit the event loop knows
// of alone, at most maxBytes of them but always one when any is due.
// from below the first entry the log keeps is COMPACTED: the reader
// starts again from a snapshot. It reads the log store concurrently
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
	// Not the loop's commit: it runs ahead of the store. A follower commits
	// entries it truncated and appended in memory before the storage actor
	// rewrote the suffix, and an install moves the commit before the store
	// is moved onto the snapshot; the store would hand back the old suffix
	// as committed (#400). The bound is what the store itself holds as
	// committed: its own commit, written with the entries it covers, or the
	// last entry applied from the log, synced at its term before it was
	// applied. Both are at or below the loop's commit, and a committed entry
	// is never truncated.
	hs, err := n.cfg.Log.HardState()
	if err != nil {
		return nil, err
	}
	to = min(to, max(hs.Commit, n.logApplied.Load()))
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

// Read implements io.Reader: the chunk reader's bytes, and at its end the
// sizes and the SHA-256 compared with the metadata, SNAPSHOT_CORRUPT on a
// difference instead of io.EOF.
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

// ImportParams are what ImportObserved rebuilds a state from: the
// snapshot an observer saved, the committed tail it read after it, and the
// state machine that replays the one over the other. Snapshot and Machine
// are required.
type ImportParams struct {
	// Snapshot is the saved snapshot's encoded stream, as SaveSnapshot wrote it.
	Snapshot io.Reader
	// Tail is the committed entries read after the snapshot, in log order.
	Tail []coreconsensus.EntryValue
	// Machine is a fresh state machine; the snapshot is restored into it and the tail applied.
	Machine coreconsensus.StateMachine
}

// ImportObserved starts a new history on an empty, joining directory from
// a saved snapshot and the committed tail an observer read after it: the
// tail is checked against the snapshot (consecutive indexes from its
// position, terms that never decrease, each payload's BodySum and the
// ChainSum chained from the snapshot's ChainTip), replayed through
// p.Machine from the snapshot, and the state it reaches is restored as
// RestoreSnapshot does, its last entry the index the new history kept. An
// empty tail is RestoreSnapshot itself. opt.Expected is the snapshot's
// metadata.
//
// A tail that raises the cluster version, changes the timing contract or
// the no-space alarm is refused: the restored metadata could not carry it;
// import from a snapshot cut after it. Membership entries are checked and
// passed over: the new history has its own members.
func ImportObserved(d DataDir, p *ImportParams, newCluster coreconsensus.ClusterID, members []coreconsensus.MemberValue, opt *RestoreOptions) (coreconsensus.SnapshotMetaValue, error) {
	if opt == nil || p == nil || p.Snapshot == nil || p.Machine == nil {
		return coreconsensus.SnapshotMetaValue{}, restoreRefused("no options, no snapshot or no state machine")
	}
	if len(p.Tail) == 0 {
		return RestoreSnapshot(d, p.Snapshot, newCluster, members, opt)
	}
	if err := checkObservedTail(&opt.Expected, p.Tail); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	codec, err := restoreCodec(opt)
	if err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	if err := replayObserved(p, opt); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	encoded, meta, err := encodeObserved(p, codec, opt.Expected)
	if err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	o := *opt
	o.Expected = meta
	return RestoreSnapshot(d, bytes.NewReader(encoded), newCluster, members, &o)
}

// replayObserved restores p.Snapshot into p.Machine, reads the stream to its
// digest whatever the machine left unread, then applies the tail's commands
// with the tokens the snapshot's lineage gives them.
func replayObserved(p *ImportParams, opt *RestoreOptions) error {
	r, err := OpenSnapshot(p.Snapshot, opt)
	if err != nil {
		return err
	}
	exp := opt.Expected
	if err := p.Machine.Restore(r, exp); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		return err
	}
	for i := range p.Tail {
		e := p.Tail[i]
		if e.Kind != coreconsensus.EntryCommand {
			continue
		}
		e.Token = coreconsensus.TokenValue{Cluster: exp.Log.Cluster, Epoch: exp.Log.Epoch, Term: e.Position.Term, Index: e.Position.Index}
		p.Machine.Apply(e)
	}
	return nil
}

// encodeObserved encodes the state p.Machine reached as a snapshot stream,
// and the metadata that describes it: exp's, moved to the tail's last entry.
func encodeObserved(p *ImportParams, codec coretransform.BoundedDecompressor, exp coreconsensus.SnapshotMetaValue) ([]byte, coreconsensus.SnapshotMetaValue, error) {
	s, err := p.Machine.Snapshot()
	if err != nil {
		return nil, coreconsensus.SnapshotMetaValue{}, err
	}
	defer s.Release()
	var buf bytes.Buffer
	cw := newChunkWriter(&buf, codec, defaultChunkSize)
	if _, err := s.WriteTo(cw); err != nil {
		return nil, coreconsensus.SnapshotMetaValue{}, err
	}
	if err := cw.Close(); err != nil {
		return nil, coreconsensus.SnapshotMetaValue{}, err
	}
	last := p.Tail[len(p.Tail)-1]
	meta := exp
	meta.ID = fmt.Sprintf("%020d-%020d", last.Position.Index, last.Position.Term)
	meta.Position, meta.ChainTip = last.Position, last.ChainSum
	meta.Codec, meta.ChunkSize = string(codec.Algorithm()), defaultChunkSize
	meta.EncodedSize, meta.DecodedSize = cw.encoded, cw.decoded
	copy(meta.SHA256[:], cw.sum.Sum(nil))
	meta.Membership.Index = last.Position.Index
	return buf.Bytes(), meta, nil
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
