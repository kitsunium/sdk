package consensus

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// A node presented as a stream (#832): one group is one totally ordered
// log, so the stream has one partition and its offsets are the log's
// indexes. A record is one command entry, framed so that a reader tells it
// from the group's other entries — the leader's no-op, a membership change,
// an administrative entry, a command another writer proposed — and skips
// them, as a reader skips a compacted offset. Retention is the node's own
// compaction: below the log's first index a read is OFFSET_TRIMMED.
const (
	// recordMagic and recordVersion open every record entry.
	recordMagic   byte = 0xFA
	recordVersion byte = 1
	// recordHeaderBytes is the most a record's frame adds to its key and
	// payload: magic, version, the append instant and the key's length.
	recordHeaderBytes int = 2 + 8 + binary.MaxVarintLen64
	// maxStreamReadBytes bounds what one ReadCommitted of a Read asks for.
	maxStreamReadBytes int = 4 << 20
)

// StreamConfig configures NewStream: the record bound and the clock that
// stamps every batch.
type StreamConfig struct {
	// Clock stamps each batch's AppendedAt on the node that appends it; the
	// instant travels IN the entry, so every node reads the same one. Nil is
	// clock.System.
	Clock clock.Clock
	// MaxRecordBytes bounds one record, its key and payload together. Zero is
	// the smaller of corestream.DefaultMaxRecordBytes and what the node's
	// MaxEntryBytes leaves after the record's frame; negative, or above that
	// room, is STREAM_MISCONFIGURED.
	MaxRecordBytes int
}

// NewStream presents node as a replicated corestream.Stream of one partition
// whose offsets are the log's indexes (#832). Append proposes each record as
// one entry, in order, the next once the previous is applied here; Read
// reads what this node applied, on a leader or a follower alike, and skips
// the entries that are not records; Bounds is the log's first index and the
// next index after the applied one. The stream implements corestream.Waker
// from the node's status: a wake follows every apply and leadership change.
// It keeps no cursor: a consumer's Committer is its own store, never the
// log. The node stays the caller's to close.
func NewStream(node *Node, cfg StreamConfig) (corestream.Stream, error) {
	if node == nil {
		return nil, streamMisconfigured("node", "nil")
	}
	room := node.cfg.MaxEntryBytes - recordHeaderBytes
	switch {
	case cfg.MaxRecordBytes < 0:
		return nil, streamMisconfigured("MaxRecordBytes", "negative")
	case room < 1:
		return nil, streamMisconfigured("MaxRecordBytes", "the node's MaxEntryBytes leaves no room for a record")
	case cfg.MaxRecordBytes > room:
		return nil, streamMisconfigured("MaxRecordBytes", "above the node's MaxEntryBytes less the record's frame")
	case cfg.MaxRecordBytes == 0:
		cfg.MaxRecordBytes = min(corestream.DefaultMaxRecordBytes, room)
	}
	clk := cfg.Clock
	if clk == nil {
		clk = clock.System
	}
	return &nodeStream{node: node, clk: clk, maxRecord: cfg.MaxRecordBytes, wake: make(chan struct{})}, nil
}

// nodeStream is NewStream's stream.
type nodeStream struct {
	node      *Node
	clk       clock.Clock
	maxRecord int
	// watch starts, at the first Wake, the goroutine that turns the node's
	// status into wakes; it ends when the node's event loop does.
	watch sync.Once
	// mu guards wake and ended.
	mu    sync.Mutex
	wake  chan struct{}
	ended bool
}

// Partitions is one: a group is one totally ordered log, and more
// partitions are more groups.
func (s *nodeStream) Partitions() int { return 1 }

// Append proposes the records in order, one entry each, all stamped with one
// instant. A failure after the first record leaves the records before it in
// the log: the error says how many in its field written, and their offsets
// in first and last. A proposal whose outcome is unknown — its ctx ended
// after it was appended, or its leader changed — is STREAM_BACKEND_FAILED
// carrying, when this node knows where it was appended, its token in the
// field TokenOf reads: WaitApplied on it says whether it committed. Any
// other refusal of the node keeps its own code (NOT_LEADER, OVERLOADED,
// NODE_STOPPED...).
func (s *nodeStream) Append(ctx context.Context, records []corestream.RecordValue) (corestream.AppendValue, error) {
	if len(records) == 0 {
		return corestream.AppendValue{}, streamInvalidBatch("records", 0)
	}
	for i, r := range records {
		if size := len(r.Key) + len(r.Payload); size > s.maxRecord {
			return corestream.AppendValue{}, errs.Wrap(corestream.RecordTooLarge, errs.WrapParams{},
				errs.Int("index", i), errs.Int("size", size), errs.Int("max", s.maxRecord))
		}
	}
	at := s.clk.Now()
	var out corestream.AppendValue
	for i, r := range records {
		tok, known, err := s.node.proposeRecord(ctx, frameStreamRecord(at, r))
		if err != nil {
			return corestream.AppendValue{}, appendFailed(err, i, out, tok, known)
		}
		if i == 0 {
			out.First = corestream.Offset(tok.Index)
		}
		out.Last = corestream.Offset(tok.Index)
	}
	s.signal()
	return out, nil
}

// proposeRecord proposes one record entry and returns its token; on an
// OUTCOME_UNKNOWN, known says whether the token is where this node appended
// it, read from the proposal Propose does not hand back.
func (n *Node) proposeRecord(ctx context.Context, data []byte) (tok coreconsensus.TokenValue, known bool, err error) {
	e := coreconsensus.EntryValue{Kind: coreconsensus.EntryCommand, BodySum: wire.BodySum(data), Data: data}
	p := &proposal{entry: e, done: make(chan propResult, 1)}
	r := n.submitProposal(ctx, p)
	tok, err = r.tok, r.err
	if err == nil {
		return tok, true, nil
	}
	if errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) && p.stage.Load() == stageAppended {
		// The stage is stored after the position: reading it appended
		// orders the read of pos after the loop's write.
		return n.token(p.pos), true, err
	}
	return coreconsensus.TokenValue{}, false, err
}

// appendFailed is the error of an Append whose record index failed with err,
// done holding the offsets of the records before it.
func appendFailed(err error, index int, done corestream.AppendValue, tok coreconsensus.TokenValue, known bool) error {
	fields := []errs.FieldValue{errs.String("op", "append"), errs.Int("written", index)}
	if index > 0 {
		fields = append(fields, streamOffsetField("first", done.First), streamOffsetField("last", done.Last))
	}
	if errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) {
		reason, _ := errs.ReasonOf(err)
		fields = append(fields, errs.String("cause", reason))
		if known {
			fields = append(fields, tokenField(tok))
		}
		return errs.Wrap(corestream.StreamBackendFailed, errs.WrapParams{}, fields...)
	}
	if index == 0 && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		// Certainly not appended: the caller's own cause, as everywhere.
		return err
	}
	return errs.Wrap(err, corestream.StreamBackendFailed.Params(), fields...)
}

// Read returns the records this node applied from offset from on, at most
// max, skipping the entries that are not records.
func (s *nodeStream) Read(ctx context.Context, partition int, from corestream.Offset, max int) ([]corestream.EntryValue, error) {
	if partition != 0 {
		return nil, streamUnknownPartition(partition)
	}
	if max <= 0 {
		return nil, streamInvalidBatch("max", max)
	}
	b, err := s.Bounds(ctx, partition)
	if err != nil {
		return nil, err
	}
	switch {
	case from < b.First:
		return nil, streamOffsetTrimmed(from, b.First)
	case from > b.Next:
		return nil, errs.Wrap(corestream.OffsetAhead, errs.WrapParams{},
			streamOffsetField("offset", from), streamOffsetField("next", b.Next))
	}
	return s.readFrom(ctx, uint64(from), uint64(b.Next)-1, max)
}

// readFrom reads the records of the entries lo through last, at most max,
// as Read does once it checked its offset against the bounds.
func (s *nodeStream) readFrom(ctx context.Context, lo, last uint64, max int) ([]corestream.EntryValue, error) {
	var out []corestream.EntryValue
	for lo <= last && len(out) < max {
		left := max - len(out)
		hi := min(last, lo+uint64(left)-1)
		budget := min(maxStreamReadBytes, left*(s.maxRecord+recordHeaderBytes))
		ents, err := s.node.ReadCommitted(ctx, lo, hi, budget)
		if errs.HasCode(err, coreconsensus.CodeCompacted) {
			return s.compactedAt(out, lo)
		}
		if err != nil {
			return nil, errs.Wrap(err, corestream.StreamBackendFailed.Params(), errs.String("op", "read"))
		}
		if len(ents) == 0 {
			break
		}
		out = appendStreamRecords(out, ents)
		lo = ents[len(ents)-1].Position.Index + 1
	}
	return out, nil
}

// compactedAt answers a read the log was compacted under at lo: what was
// read before it, the next Read reporting the gap, or OFFSET_TRIMMED when
// nothing was.
func (s *nodeStream) compactedAt(out []corestream.EntryValue, lo uint64) ([]corestream.EntryValue, error) {
	if len(out) > 0 {
		return out, nil
	}
	first, _ := s.node.cfg.Log.Bounds()
	return nil, streamOffsetTrimmed(corestream.Offset(lo), corestream.Offset(first))
}

// appendStreamRecords appends to out the records among ents, skipping the
// entries that are not records.
func appendStreamRecords(out []corestream.EntryValue, ents []coreconsensus.EntryValue) []corestream.EntryValue {
	for _, e := range ents {
		if v, ok := readStreamRecord(e); ok {
			out = append(out, v)
		}
	}
	return out
}

// Bounds is the log's first index and the index after the one this node
// applied last.
func (s *nodeStream) Bounds(ctx context.Context, partition int) (corestream.BoundsValue, error) {
	if partition != 0 {
		return corestream.BoundsValue{}, streamUnknownPartition(partition)
	}
	if err := ctx.Err(); err != nil {
		return corestream.BoundsValue{}, err
	}
	if s.node.stopped.Load() {
		return corestream.BoundsValue{}, errs.Wrap(s.node.stoppedErr(), corestream.StreamBackendFailed.Params(), errs.String("op", "bounds"))
	}
	next := s.node.Status().Applied + 1
	first, _ := s.node.cfg.Log.Bounds()
	return corestream.BoundsValue{First: corestream.Offset(min(first, next)), Next: corestream.Offset(next)}, nil
}

// Wake implements corestream.Waker: the channel is closed by the next
// status the node publishes — an apply, a commit, a leadership change — or
// by the next Append through this stream, and stays closed once the node's
// event loop has ended. The first call launches the goroutine that signals
// each status: it runs until the node's event loop ends, which closes its
// watch, and signals once more on its way out, the last wake it closes.
func (s *nodeStream) Wake() <-chan struct{} {
	s.watch.Do(func() {
		ch := s.node.Watch(context.Background())
		go func() {
			defer func() {
				// The last signal, as signal closes and re-arms the wake
				// under the same lock and test; ended then answers every
				// later Wake with a closed channel.
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.ended {
					return
				}
				close(s.wake)
				s.wake = make(chan struct{})
				s.ended = true
			}()
			for range ch {
				s.signal()
			}
		}()
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return closedWake()
	}
	return s.wake
}

// closedWake is the wake of a stream whose node ended: closed already, as
// it stays.
func closedWake() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// signal closes the wake handed out and arms the next one, unless the node
// ended and every wake is closed.
func (s *nodeStream) signal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	close(s.wake)
	s.wake = make(chan struct{})
}

// frameStreamRecord frames r as a record entry appended at at: magic, version,
// the instant in Unix nanoseconds, the key's length, the key, the payload.
func frameStreamRecord(at time.Time, r corestream.RecordValue) []byte {
	b := make([]byte, 0, recordHeaderBytes+len(r.Key)+len(r.Payload))
	b = append(b, recordMagic, recordVersion)
	b = binary.BigEndian.AppendUint64(b, uint64(at.UnixNano()))
	b = binary.AppendUvarint(b, uint64(len(r.Key)))
	b = append(b, r.Key...)
	return append(b, r.Payload...)
}

// readStreamRecord reads a record entry, false for any entry that is not one.
// The record owns a copy of the bytes: the log store may share its own.
func readStreamRecord(e coreconsensus.EntryValue) (corestream.EntryValue, bool) {
	d := e.Data
	if e.Kind != coreconsensus.EntryCommand || len(d) < 10 || d[0] != recordMagic || d[1] != recordVersion {
		return corestream.EntryValue{}, false
	}
	at := time.Unix(0, int64(binary.BigEndian.Uint64(d[2:10])))
	klen, k := binary.Uvarint(d[10:])
	if k <= 0 || klen > uint64(len(d)-10-k) {
		return corestream.EntryValue{}, false
	}
	body := slices.Clone(d[10+k:])
	var r corestream.RecordValue
	if klen > 0 {
		r.Key = body[:klen:klen]
	}
	if int(klen) < len(body) {
		r.Payload = body[klen:]
	}
	return corestream.EntryValue{AppendedAt: at, Record: r, Partition: 0, Offset: corestream.Offset(e.Position.Index)}, true
}

// tokenField is a token in the hex field TokenOf decodes.
func tokenField(t coreconsensus.TokenValue) errs.FieldValue {
	return errs.String("token", hex.EncodeToString(wire.AppendToken(nil, t)))
}

// streamOffsetField renders an offset as a field, in decimal, exactly.
func streamOffsetField(key string, o corestream.Offset) errs.FieldValue {
	return errs.String(key, strconv.FormatUint(uint64(o), 10))
}

// streamMisconfigured refuses a NewStream whose field is wrong for why.
func streamMisconfigured(field, why string) error {
	return errs.Wrap(corestream.StreamMisconfigured, errs.WrapParams{}, errs.String("field", field), errs.String("why", why))
}

// streamUnknownPartition refuses any partition but 0.
func streamUnknownPartition(partition int) error {
	return errs.Wrap(corestream.UnknownPartition, errs.WrapParams{}, errs.Int("partition", partition), errs.Int("partitions", 1))
}

// streamInvalidBatch refuses an empty batch or a non-positive read size.
func streamInvalidBatch(field string, value int) error {
	return errs.Wrap(corestream.InvalidBatchSize, errs.WrapParams{}, errs.String("field", field), errs.Int("value", value))
}

// streamOffsetTrimmed refuses a read from offset, below first.
func streamOffsetTrimmed(offset, first corestream.Offset) error {
	return errs.Wrap(corestream.OffsetTrimmed, errs.WrapParams{}, streamOffsetField("offset", offset), streamOffsetField("first", first))
}
