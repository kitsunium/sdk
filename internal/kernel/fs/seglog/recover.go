package seglog

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// scanRec is what recovery keeps of a record past the scan: everything but
// its payload.
type scanRec struct {
	off    int64
	batch  uint64
	index  uint64
	length uint32
	size   uint32
	kind   uint8
	tag    uint8
	end    bool
}

// scanRecOf is the scanRec of r, parsed at offset off.
func scanRecOf(r *record, off int64) scanRec {
	return scanRec{
		off: off, batch: r.batch, index: r.index, length: uint32(r.length), size: uint32(len(r.data)),
		kind: r.kind, tag: r.tag, end: r.end,
	}
}

// scanned is one segment as recovery read it, before anything is changed.
type scanned struct {
	n    uint64
	size int64
	// data is the bytes read of the last segment, nil for the others: the
	// whole file, unless it is sealed and was read up to its seal only.
	data []byte
	recs []scanRec
	// cp is the checkpoint the segment opens with; cpOK is false when its
	// first record holds no checkpoint payload.
	cp   checkpoint
	cpOK bool
	// layout is the VersionUnsupported of the segment's first record of a
	// kind this build does not read, nil when there is none.
	layout error
	// seqBreak is the offset of the first record that does not continue
	// the batch sequence of the log read before it, -1 when none.
	seqBreak int64
	hole     int64 // -1 when none
	sealed   bool
	trimmed  int64 // truncation point decided for the last segment, -1 none
}

// note appends to recs the scanRec of r, parsed at offset off, and judges
// what only its payload tells: the checkpoint the segment opens with, and a
// record of a kind this build does not read.
func (s *scanned) note(recs []scanRec, r *record, off int64) []scanRec {
	if len(recs) == 0 && r.kind == recCheckpt {
		s.cp, s.cpOK = decodeCheckpoint(r.data)
	}
	if s.layout == nil && !knownKind(r.kind) {
		s.layout = origin(VersionUnsupported, errs.String("segment", segName(s.n)),
			errs.Int64("offset", off), errs.Int("record_kind", int(r.kind)))
	}
	if len(recs) == cap(recs) {
		recs = slices.Grow(recs, max(len(recs), 64))
	}
	return append(recs, scanRecOf(r, off))
}

// unknownVersionAt looks, at every record boundary from off to the end of
// b, for a record whose crc holds but whose version this build does not
// write. Such a record is no damage: a newer build wrote it, synced, and
// may have acknowledged it; taken for a hole it would be zeroed as a torn
// tail. Unknown means stop, never skip.
func unknownVersionAt(b []byte, seg uint64, off int64) error {
	for o := int(off) &^ (recAlign - 1); o+recHeader+recFixed <= len(b); o += recAlign {
		if body := sealedBody(b[o:]); body != nil && body[0] != recVersion {
			return origin(VersionUnsupported, errs.String("segment", segName(seg)),
				errs.Int64("offset", int64(o)), errs.Int("record_version", int(body[0])))
		}
	}
	return nil
}

// recover scans every segment, classifies every hole, and only then changes
// anything: a torn tail zeroed, the next segment published.
func (l *Log) recover() error {
	nums, err := l.segmentNumbers()
	if err != nil {
		return err
	}
	if len(nums) == 0 {
		if err := l.createSegment(1, l.segBytes, &checkpoint{nextBatch: 1}); err != nil {
			return err
		}
		l.markDurable()
		return nil
	}
	return l.recoverExisting(nums)
}

// recoverExisting recovers a directory holding the segments nums: it
// checks them, replays them, and reopens the log at the end of the last.
func (l *Log) recoverExisting(nums []uint64) error {
	segs, err := l.scanAndTrim(nums)
	if err != nil {
		return err
	}
	last := segs[len(segs)-1]
	if err := l.replay(segs); err != nil {
		return err
	}
	if err := l.openSegments(segs); err != nil {
		return err
	}
	if err := l.syncAtOpen(); err != nil {
		return err
	}
	if err := l.positionTail(last, l.segs[len(l.segs)-1]); err != nil {
		return err
	}
	l.batch++
	l.markDurable()
	if last.sealed {
		// A crash after the seal and before the next segment's rename: the
		// seal is durable, so only the publication is left.
		return l.publishNext(0)
	}
	l.prepare(last.n+1, l.segBytes)
	return nil
}

// scanAndTrim scans the segments nums, refuses the ones whose batches or
// layout do not hold together, and zeroes the torn tail of the last, which
// is the only change recovery makes before it has judged them all.
func (l *Log) scanAndTrim(nums []uint64) ([]*scanned, error) {
	segs, err := scanSegments(l.dir, nums, &scanBufs{})
	if err != nil {
		return nil, err
	}
	if err := checkBatchSequence(segs); err != nil {
		return nil, err
	}
	if err := checkLayouts(segs); err != nil {
		return nil, err
	}
	last := segs[len(segs)-1]
	if err := trimTornTail(last); err != nil {
		return nil, err
	}
	if last.trimmed >= 0 {
		l.tornTails++
	}
	return segs, nil
}

// segmentNumbers lists the published segments, sorted, deleting on the way
// every .tmp: only an incomplete one exists, one that never reached its
// rename. A name that is not exactly a segment's is ignored.
func (l *Log) segmentNumbers() ([]uint64, error) {
	names, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, failed("readdir", err)
	}
	var nums []uint64
	for _, e := range names {
		name := e.Name()
		if strings.HasPrefix(name, "seg-") && strings.HasSuffix(name, ".tmp") {
			if err := os.Remove(filepath.Join(l.dir, name)); err != nil {
				return nil, failed("remove tmp", err)
			}
			continue
		}
		var n uint64
		if _, err := fmt.Sscanf(name, "seg-%016x", &n); err == nil && name == segName(n) {
			nums = append(nums, n)
		}
	}
	slices.Sort(nums)
	return nums, nil
}

// scanSegments reads and parses every segment of dir in order. A segment is
// judged as soon as it is read, so the first damage found is the one
// reported, and nothing after it is read. Only the last segment's bytes are
// kept.
func scanSegments(dir string, nums []uint64, sb *scanBufs) ([]*scanned, error) {
	segs := make([]*scanned, len(nums))
	var seq batchSeq
	for i, n := range nums {
		s, err := scanSegment(dir, n, sb)
		if err != nil {
			return nil, err
		}
		s.seqBreak = seq.follow(s.recs)
		s.recs = slices.Clone(s.recs)
		if i < len(nums)-1 {
			// The next segment is read into the same buffer.
			s.data = nil
		} else {
			s.data = slices.Clone(s.data)
		}
		segs[i] = s
		if len(s.recs) == 0 || s.recs[0].kind != recCheckpt {
			return nil, damaged(n, 0, "invalid checkpoint")
		}
		if i < len(nums)-1 && !s.sealed {
			return nil, damaged(n, s.hole, "hole in a sealed segment")
		}
	}
	return segs, nil
}

// scanSegment reads segment n of dir and parses its records up to the
// first hole, or through its seal: a sealed segment is read up to its seal,
// and the rest of a segment is read only for a hole, which is classified on
// the whole file. The bytes and the records go to sb's buffers, which the
// next segment reuses.
func scanSegment(dir string, n uint64, sb *scanBufs) (*scanned, error) {
	f, err := os.Open(filepath.Join(dir, segName(n)))
	if err != nil {
		return nil, failed("read segment", err)
	}
	// Read-only: there is nothing a failed close could lose.
	defer func() { abandon(f.Close()) }()
	reader, err := sb.reader(f)
	if err != nil {
		return nil, failed("read segment", err)
	}
	s := &scanned{n: n, size: int64(len(reader.b)), seqBreak: -1, hole: -1, trimmed: -1}
	recs, err := s.parse(reader, sb.recs[:0])
	if err != nil {
		return nil, err
	}
	sb.recs = recs
	s.recs = recs
	s.data = reader.b[:reader.n]
	return s, nil
}

// parse appends to recs the records of s read through reader, up to the
// first hole or through the seal, and returns them.
func (s *scanned) parse(reader *segRead, recs []scanRec) ([]scanRec, error) {
	for off := int64(0); off < s.size; {
		if err := reader.record(off); err != nil {
			return nil, failed("read segment", err)
		}
		r, ok := parseRecord(reader.b[off:reader.n])
		if !ok {
			return recs, s.holeAt(reader, off)
		}
		recs = s.note(recs, &r, off)
		off += int64(r.length)
		if r.kind == recSeal {
			s.sealed = true
			break
		}
	}
	return recs, nil
}

// holeAt records the hole at off once the rest of the file is read, which
// is where it is classified. A record a newer build wrote, here or past the
// hole, stops the open before anything is classified or changed.
func (s *scanned) holeAt(reader *segRead, off int64) error {
	if err := reader.upTo(s.size); err != nil {
		return failed("read segment", err)
	}
	if err := unknownVersionAt(reader.b, s.n, off); err != nil {
		return err
	}
	s.hole = off
	return nil
}

// scanBufs is what the scan reuses from one segment to the next: the
// buffer a segment is read into and the records parsed from it. chunk is
// how far ahead it reads, scanChunk when zero; only a test sets it.
type scanBufs struct {
	b     []byte
	recs  []scanRec
	chunk int64
}

// reader is the read of segment file f into sb's buffer, grown when the
// file is larger than any before it. A file larger than an int holds is
// refused before the buffer is made.
func (sb *scanBufs) reader(f statReader) (*segRead, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size > math.MaxInt {
		return nil, errSegmentTooLarge
	}
	if int64(cap(sb.b)) < size {
		sb.b = make([]byte, 0, size)
	}
	chunk := sb.chunk
	if chunk <= 0 {
		chunk = scanChunk
	}
	return &segRead{f: f, b: sb.b[:size], chunk: chunk}, nil
}

// statReader is a segment file as the scan sees it: something to read and
// to size.
type statReader interface {
	io.Reader
	Stat() (os.FileInfo, error)
}

// segRead is a segment file read in order into b, the file's size, of
// which the first n bytes are read, chunk at least at a time. What lies
// past n is an earlier segment's bytes, and no parse looks at it.
type segRead struct {
	f     io.Reader
	b     []byte
	n     int64
	chunk int64
}

// record reads enough for parseRecord to judge the record at off on
// b[off:n] as it would on the whole file.
func (r *segRead) record(off int64) error {
	if err := r.upTo(off + int64(recHeader+recFixed)); err != nil {
		return err
	}
	if off+int64(recHeader) > r.n {
		return nil
	}
	return r.upTo(off + int64(recHeader) + int64(binary.LittleEndian.Uint32(r.b[off:])))
}

// upTo reads the file up to end, at most its size, a chunk at least at a
// time.
func (r *segRead) upTo(end int64) error {
	size := int64(len(r.b))
	end = min(end, size)
	if r.n >= end {
		return nil
	}
	to := min(size, max(end, r.n+r.chunk))
	if _, err := io.ReadFull(r.f, r.b[r.n:to]); err != nil {
		return err
	}
	r.n = to
	return nil
}

// checkBatchSequence refuses a log whose batch sequence is not contiguous
// across the whole log, at the first record that breaks it.
func checkBatchSequence(segs []*scanned) error {
	for _, s := range segs {
		if s.seqBreak >= 0 {
			return damaged(s.n, s.seqBreak, "batch sequence")
		}
	}
	return nil
}

// checkLayouts refuses, before any tail is classified or trimmed, a record
// of a kind this build does not read: left to replay, one in an unfinished
// last batch would be trimmed with the tear and zeroed.
func checkLayouts(segs []*scanned) error {
	for _, s := range segs {
		if s.layout != nil {
			return s.layout
		}
	}
	return nil
}

// batchSeq follows the batch sequence across the log, segment after
// segment.
type batchSeq struct {
	prev scanRec
	any  bool
}

// follow returns the offset of the first record of recs that does not
// continue the sequence read so far, -1 when every one does.
func (q *batchSeq) follow(recs []scanRec) int64 {
	for i := range recs {
		r := &recs[i]
		if q.any && !continuesBatch(&q.prev, r) {
			return r.off
		}
		q.prev, q.any = *r, true
	}
	return -1
}

// continuesBatch reports whether r may follow prev: in prev's batch, or
// first of the next one once prev ended its own.
func continuesBatch(prev, r *scanRec) bool {
	return r.batch == prev.batch || (r.batch == prev.batch+1 && prev.end)
}

// trimTornTail decides the fate of a hole in the last segment: a torn tail
// is dropped from the records, its truncation point kept for positionTail
// to zero and sync before any new record; any other hole is Damaged.
func trimTornTail(last *scanned) error {
	if last.hole < 0 {
		return nil
	}
	trim, why := classifyTail(last.data, last.recs, last.hole)
	if trim < 0 {
		return damaged(last.n, last.hole, why)
	}
	k := len(last.recs)
	for k > 0 && last.recs[k-1].off >= trim {
		k--
	}
	last.recs = last.recs[:k]
	last.trimmed = trim
	return nil
}

// classifyTail returns where to truncate a torn tail in the segment bytes
// b, whose valid records before the hole are recs, or -1 and why the hole
// is damage.
func classifyTail(b []byte, recs []scanRec, hole int64) (int64, string) {
	rb := recs[len(recs)-1]
	after := recordsAfter(b, hole)
	// The batch the hole sits in: rb's, unless rb ended its batch.
	torn := rb.batch
	if rb.end {
		torn = rb.batch + 1
	}
	if why := judgeAfterHole(after, torn); why != "" {
		return -1, why
	}
	if torn == rb.batch {
		return tornBatchStart(recs, rb.off, torn)
	}
	return hole, ""
}

// recordsAfter is every valid record after the hole, at record boundaries.
func recordsAfter(b []byte, hole int64) []record {
	var after []record
	for off := align8(int(hole) + recAlign); off+recHeader+recFixed <= len(b); off += recAlign {
		if r, ok := parseRecord(b[off:]); ok {
			after = append(after, r)
			off += r.length - recAlign
		}
	}
	return after
}

// judgeAfterHole returns why a hole is damage given the valid records after
// it, or "" when they do not forbid a tear.
func judgeAfterHole(after []record, torn uint64) string {
	for _, r := range after {
		if r.batch != torn {
			// A valid record of another batch after a hole: never a tear.
			return "valid record of another batch after a hole"
		}
		if r.end {
			// The torn batch's end record IS valid: it may have been
			// acknowledged, so damage inside it is damage.
			return "damage inside a batch whose end record is valid"
		}
	}
	return ""
}

// tornBatchStart is where the torn batch, which never completed, begins:
// all of it is dropped, as it was never synced and never acknowledged. A
// torn batch that starts with the segment's checkpoint is damage.
func tornBatchStart(recs []scanRec, lastOff int64, torn uint64) (int64, string) {
	start := lastOff
	for i := len(recs) - 1; i >= 0 && recs[i].batch == torn; i-- {
		start = recs[i].off
	}
	if recs[0].batch == torn && start == recs[0].off {
		return -1, "torn checkpoint"
	}
	return start, ""
}

// replay builds the log's index from every segment's checkpoint and
// records, in order, and judges continuity on the result.
func (l *Log) replay(segs []*scanned) error {
	n := 0
	for _, s := range segs {
		n += len(s.recs)
	}
	l.slots = make([]slot, 0, n)
	for si, s := range segs {
		if !s.cpOK {
			return damaged(s.n, 0, "checkpoint payload")
		}
		if si == 0 {
			l.first, l.next = s.cp.next, s.cp.next
		} else if s.cp.next != l.next {
			return damaged(s.n, 0, "checkpoint does not continue the log")
		}
		l.replayDrop(s.cp.first)
		for i := 1; i < len(s.recs); i++ {
			if err := l.replayRecord(s.n, &s.recs[i]); err != nil {
				return err
			}
			l.batch = s.recs[i].batch
		}
		l.batch = max(l.batch, s.recs[0].batch)
	}
	// Copied at their count: the drops resliced the front away.
	l.slots = slices.Clone(l.slots)
	return nil
}

// replayRecord applies one record of segment seg to the index being built.
func (l *Log) replayRecord(seg uint64, r *scanRec) error {
	switch r.kind {
	case recEntry:
		if r.index != l.next {
			return damaged(seg, r.off, "index out of sequence")
		}
		l.slots = append(l.slots, slot{seg: seg, off: r.off, size: r.size, kind: r.tag})
		l.next++
	case recDrop:
		l.replayDrop(r.index)
	case recCheckpt:
		return damaged(seg, r.off, "checkpoint inside a segment")
	}
	return nil
}

// replayDrop is applyDrop during the replay: the front resliced away, not
// copied, which replay does once at its end.
func (l *Log) replayDrop(index uint64) {
	switch {
	case index <= l.first:
	case index >= l.next:
		l.slots = l.slots[:0]
		l.first, l.next = index, index
	default:
		l.slots = l.slots[index-l.first:]
		l.first = index
	}
}

// openSegments opens every scanned segment for reading and writing, in
// order; those already opened stay in l.segs for Close.
func (l *Log) openSegments(segs []*scanned) error {
	for _, s := range segs {
		f, err := os.OpenFile(l.segPath(s.n), os.O_RDWR, 0)
		if err != nil {
			return failed("open segment", err)
		}
		l.segs = append(l.segs, &segment{n: s.n, f: f, size: s.size, start: s.cp.next})
	}
	return nil
}

// syncAtOpen makes durable everything recovery is about to trust, before
// anything is reported from it. A process killed between a batch's write
// and its sync leaves that batch in the page cache, complete with its end
// record: a restart in the same boot reads it as written, and would report
// records no device holds. The same holds for a rename or an unlink whose
// directory sync never ran.
func (l *Log) syncAtOpen() error {
	for _, s := range l.segs {
		if err := datasync(s.f); err != nil {
			return failed("sync at open", err)
		}
	}
	if err := syncDir(l.dir); err != nil {
		return failed("sync at open", err)
	}
	return nil
}

// positionTail sets the write offset in the last segment: at the torn
// tail's truncation point, zeroing and syncing what lies past it unless it
// is zero already, else right after the last valid record.
func (l *Log) positionTail(last *scanned, tail *segment) error {
	switch {
	case last.trimmed >= 0:
		l.off = last.trimmed
		if !allZero(last.data[l.off:]) {
			if err := zeroRange(tail.f, l.off, tail.size); err != nil {
				return failed("zero tail", err)
			}
			if err := datasync(tail.f); err != nil {
				return failed("zero tail", err)
			}
		}
	case len(last.recs) > 0:
		lr := last.recs[len(last.recs)-1]
		l.off = lr.off + int64(lr.length)
	}
	return nil
}
