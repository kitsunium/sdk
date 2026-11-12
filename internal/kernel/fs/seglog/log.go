package seglog

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// slot locates one retained entry: in the pending batch before its sync,
// in segment seg after.
type slot struct {
	seg     uint64
	off     int64
	size    uint32
	kind    uint8
	pending bool
}

// durable is the state the synced records describe, which a rotation's
// checkpoint is written from, never from the pending batch it precedes.
type durable struct {
	next  uint64
	first uint64
}

// Log is a segmented log opened by Open. One writer at a time — Append, Sync,
// DropBefore and Close serialise — and any number of readers, which never wait
// for a sync's disk I/O. The zero value is NOT usable.
type Log struct {
	dir      string
	segBytes int64

	// mu guards the state below; readers hold it shared.
	mu   sync.RWMutex
	segs []*segment
	// off is the write offset in the last segment.
	off int64
	// batch is the sequence number of the pending batch.
	batch   uint64
	pending []byte
	// pendingLast is the offset in pending of its last record, -1 for none.
	pendingLast int
	first       uint64
	next        uint64
	// slots[i] locates entry first+i.
	slots []slot
	dur   durable
	prep  *prepared
	// failed is the failure that stopped the log, nil while it is healthy.
	failed error
	closed bool
	// tornTails counts the torn tails recovery repaired; a test reads it.
	tornTails int

	// afterSeal runs once a rotation's seal is durable and before the next
	// segment is published; duringSync runs between each write of a batch
	// and its sync, the lock released. Only a test sets them.
	afterSeal  func()
	duringSync func()

	// wmu serialises the writers. A sync holds it, not mu, across its write
	// and sync, so readers do not wait for the disk.
	wmu sync.Mutex
}

// Open opens, and recovers, the log kept in dir, creating the directory (mode
// 0700) and its first segment when there is none. segBytes is the size of a
// segment, DefaultSegmentBytes when it is zero or negative; a batch larger
// than a segment gets a segment of its own size. Open refuses with Damaged or
// VersionUnsupported a log it cannot trust, having changed nothing, and with
// Failed a directory the filesystem refuses; a torn tail it repairs. Before it
// returns, every segment and the directory are synced, so nothing it reports
// rests on pages a disk did not hold.
func Open(dir string, segBytes int64) (*Log, error) {
	if segBytes <= 0 {
		segBytes = DefaultSegmentBytes
	}
	l := &Log{dir: dir, segBytes: segBytes, pendingLast: -1}
	if err := mkdirDurable(dir); err != nil {
		return nil, err
	}
	if err := l.recover(); err != nil {
		// The segments recovery opened are closed: dropped with l, they
		// would leak one descriptor each at every retry.
		abandon(l.Close())
		return nil, err
	}
	return l, nil
}

// mkdirDurable creates dir, private to the owner, and when it did not
// exist syncs its parent, which publishes the new entry.
func mkdirDurable(dir string) error {
	_, err := os.Stat(dir)
	existed := err == nil
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return failed("mkdir", err)
	}
	if existed {
		return nil
	}
	if err := syncDir(filepath.Dir(dir)); err != nil {
		return failed("mkdir", err)
	}
	return nil
}

// usable is why the log refuses a call: closed, then the failure that
// stopped it, nil while it is healthy.
func (l *Log) usable() error {
	if l.closed {
		return Closed
	}
	return l.failed
}

// add appends r to the pending batch and remembers it as the batch's last
// record, the one Sync flags end-of-batch.
func (l *Log) add(r *record) {
	l.pendingLast = len(l.pending)
	l.pending = appendRecord(l.pending, r)
}

// Append adds a record to the pending batch: payload, copied, at index, with
// the caller's kind byte. index must be the log's next (OutOfSequence
// otherwise) and payload at most 1 GiB less a record's header (TooLarge). The
// record is readable at once and durable only once Sync returns nil.
func (l *Log) Append(index uint64, kind uint8, payload []byte) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return err
	}
	if index != l.next {
		return origin(OutOfSequence, errs.String("index", u64(index)), errs.String("next", u64(l.next)))
	}
	if len(payload) > maxPayload {
		return origin(TooLarge, errs.Int("bytes", len(payload)), errs.Int("max", maxPayload))
	}
	off := len(l.pending)
	l.add(&record{kind: recEntry, tag: kind, batch: l.batch, index: index, data: payload})
	l.slots = append(l.slots, slot{off: int64(off), size: uint32(len(payload)), kind: kind, pending: true})
	l.next++
	return nil
}

// Sync makes the pending batch durable: written after the log's last record,
// every record but the last synced, then the last one, flagged end-of-batch,
// synced. A full segment is sealed and the next one published between two
// batches, never inside one. Whole segments a DropBefore left behind are then
// unlinked. A failure is final: the log refuses every later call. With nothing
// pending it returns at once.
func (l *Log) Sync() error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.syncLocked()
}

// syncLocked is Sync; the caller holds wmu and mu.
func (l *Log) syncLocked() error {
	if err := l.usable(); err != nil {
		return err
	}
	if len(l.pending) == 0 {
		return nil
	}
	l.pending[l.pendingLast+recHeader+2] = 1
	sealRecord(l.pending[l.pendingLast:])
	cur := l.segs[len(l.segs)-1]
	if l.off+int64(len(l.pending))+sealReserve > cur.size {
		// Rotation happens only between batches: restamp the batch with the
		// sequence the new segment expects.
		if err := l.rotate(len(l.pending)); err != nil {
			l.failed = err
			return err
		}
		l.restamp(l.batch)
		cur = l.segs[len(l.segs)-1]
	}
	// The writes and their syncs run without mu: the writers are held by
	// wmu, readers find the pending records in memory and the durable ones
	// below l.off, which the writes do not touch.
	l.mu.Unlock()
	err := l.writeBatch(cur)
	l.mu.Lock()
	if err != nil {
		l.failed = err
		return err
	}
	l.settleBatch(cur.n)
	if err := l.dropSegmentsLocked(); err != nil {
		l.failed = err
		return err
	}
	return nil
}

// writeBatch makes the pending batch durable at l.off in segment cur in two
// steps: every record before the last, synced, then the last one, which
// carries the end-of-batch flag, synced. A power cut during one sync may
// persist any subset of its dirty pages, the end record's without an
// earlier one's; written last and on its own, a valid end record proves the
// rest of its batch durable before it, so a hole before it is damage to
// synced data, and nothing else.
func (l *Log) writeBatch(cur *segment) error {
	body := l.pending[:l.pendingLast]
	if len(body) > 0 {
		if err := l.writeAndSync(cur, body, l.off); err != nil {
			return err
		}
	}
	return l.writeAndSync(cur, l.pending[l.pendingLast:], l.off+int64(len(body)))
}

// writeAndSync writes b at off in segment cur and syncs it.
func (l *Log) writeAndSync(cur *segment, b []byte, off int64) error {
	if _, err := cur.f.WriteAt(b, off); err != nil {
		return failed("write", err)
	}
	if l.duringSync != nil {
		l.duringSync()
	}
	if err := datasync(cur.f); err != nil {
		return failed("sync", err)
	}
	return nil
}

// settleBatch records the pending batch, now synced in segment seg, as
// durable: its entries are read from disk from here on, the write offset
// moves past it, and the next batch gets the next sequence number.
func (l *Log) settleBatch(seg uint64) {
	for i := len(l.slots) - 1; i >= 0 && l.slots[i].pending; i-- {
		s := &l.slots[i]
		s.seg, s.off, s.pending = seg, l.off+s.off, false
	}
	l.off += int64(len(l.pending))
	l.markDurable()
	l.pending = l.pending[:0]
	l.pendingLast = -1
	l.batch++
}

// markDurable records the state the synced records now describe.
func (l *Log) markDurable() { l.dur = durable{next: l.next, first: l.first} }

// restamp rewrites the batch sequence of every pending record.
func (l *Log) restamp(batch uint64) {
	var off int
	for off < len(l.pending) {
		binary.LittleEndian.PutUint64(l.pending[off+recHeader+4:], batch)
		sealRecord(l.pending[off:])
		n := int(binary.LittleEndian.Uint32(l.pending[off:]))
		off += align8(recHeader + n)
	}
}

// DropBefore makes index the log's first: the records below it become
// unreadable at once, a drop record is added to the pending batch and the batch
// is synced, as Sync syncs it, after which the sealed segments that hold only
// records below index are unlinked and the directory synced. An index past
// the next empties the log and makes it the next too, so the next Append
// takes it. An index at or below the first changes nothing.
func (l *Log) DropBefore(index uint64) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return err
	}
	if index <= l.first {
		return nil
	}
	l.add(&record{kind: recDrop, batch: l.batch, index: index})
	l.applyDrop(index)
	return l.syncLocked()
}

// applyDrop makes index the first: the slots below it go, and past the
// next the log is empty and starts there.
func (l *Log) applyDrop(index uint64) {
	if index <= l.first {
		return
	}
	if index >= l.next {
		l.slots = nil
		l.first, l.next = index, index
		return
	}
	l.slots = slices.Clone(l.slots[index-l.first:])
	l.first = index
}

// Bounds returns the log's first index and its next: the records held are
// first through next-1, none when they are equal. A fresh log holds none at
// index 0.
func (l *Log) Bounds() (first, next uint64) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.first, l.next
}

// Kind returns the caller's kind byte of the record at index, from memory,
// with Read's errors for an index outside the log.
func (l *Log) Kind(index uint64) (uint8, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if err := l.usable(); err != nil {
		return 0, err
	}
	if index < l.first || index >= l.next {
		return 0, outside(index, l.first, l.next)
	}
	return l.slots[index-l.first].kind, nil
}

// Read returns a copy of the payload at index: Dropped below the first index,
// OutOfRange at or past the next, Damaged when the record on disk no longer
// matches the one the log recorded.
func (l *Log) Read(index uint64) ([]byte, error) {
	out, err := l.Range(index, index+1, 0, nil)
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// Range appends to dst the payloads at lo up to hi excluded, and returns it:
// always the first, then as long as the payloads appended stay within
// maxBytes. Contiguous records of one segment are read with one read. The
// payloads are the caller's and may share one backing array. Errors are
// Read's, for lo and hi; an empty range returns dst.
func (l *Log) Range(lo, hi uint64, maxBytes int, dst [][]byte) ([][]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if err := l.usable(); err != nil {
		return dst, err
	}
	if lo >= hi {
		return dst, nil
	}
	if lo < l.first || lo >= l.next {
		return dst, outside(lo, l.first, l.next)
	}
	if hi > l.next {
		return dst, outside(hi-1, l.first, l.next)
	}
	n := l.cut(lo, hi, maxBytes)
	for i := 0; i < n; {
		s := l.slots[lo-l.first+uint64(i)]
		if s.pending {
			dst = append(dst, l.pendingPayload(s))
			i++
			continue
		}
		run := l.run(lo-l.first+uint64(i), n-i)
		var err error
		if dst, err = l.readRun(lo+uint64(i), l.slots[lo-l.first+uint64(i):][:run], dst); err != nil {
			return dst, err
		}
		i += run
	}
	return dst, nil
}

// cut is how many records from lo up to hi Range returns: at least one, and
// then as many as stay within maxBytes of payload.
func (l *Log) cut(lo, hi uint64, maxBytes int) int {
	n, size := 0, 0
	for i := lo; i < hi; i++ {
		size += int(l.slots[i-l.first].size)
		if n > 0 && size > maxBytes {
			break
		}
		n++
	}
	return n
}

// run is how many of the at most max slots from slot i are durable,
// contiguous and in one segment: what one read returns.
func (l *Log) run(i uint64, maxRun int) int {
	s := l.slots[i]
	end := s.off + int64(recordLen(int(s.size)))
	n := 1
	for n < maxRun {
		t := l.slots[i+uint64(n)]
		if t.pending || t.seg != s.seg || t.off != end {
			break
		}
		end += int64(recordLen(int(t.size)))
		n++
	}
	return n
}

// pendingPayload is a copy of the payload of the pending record s locates.
func (l *Log) pendingPayload(s slot) []byte {
	start := s.off + int64(recHeader+recFixed)
	return slices.Clone(l.pending[start : start+int64(s.size)])
}

// readRun reads the contiguous records slots locate, the first at index,
// with one read, checks each against what the log recorded, and appends
// their payloads to dst. A record that does not match is Damaged: the log
// disagrees with what it wrote.
func (l *Log) readRun(index uint64, slots []slot, dst [][]byte) ([][]byte, error) {
	seg := l.segment(slots[0].seg)
	if seg == nil {
		return dst, failed("read", cause("no segment holds the record"))
	}
	start := slots[0].off
	last := slots[len(slots)-1]
	end := last.off + int64(recordLen(int(last.size)))
	if end > seg.size || end-start > math.MaxInt {
		return dst, damaged(seg.n, start, "record past its segment")
	}
	b := make([]byte, end-start)
	if _, err := seg.f.ReadAt(b, start); err != nil && !errors.Is(err, io.EOF) {
		return dst, failed("read", err)
	}
	for i, s := range slots {
		at := s.off - start
		r, ok := parseRecord(b[at:])
		if !ok || r.kind != recEntry || r.index != index+uint64(i) || len(r.data) != int(s.size) {
			return dst, damaged(seg.n, s.off, "record does not match the log")
		}
		dst = append(dst, r.data[:len(r.data):len(r.data)])
	}
	return dst, nil
}

// segment is the open segment numbered n, nil when none is.
func (l *Log) segment(n uint64) *segment {
	for _, s := range l.segs {
		if s.n == n {
			return s
		}
	}
	return nil
}

// Close closes every segment and reports the first failure; a batch still
// pending is discarded, never written. Every later call returns Closed; a
// second Close returns nil.
func (l *Log) Close() error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	var err error
	for _, s := range l.segs {
		if cerr := s.f.Close(); cerr != nil && err == nil {
			err = failed("close segment", cerr)
		}
	}
	l.segs = nil
	l.slots = nil
	l.pending = nil
	if p := l.prep; p != nil {
		l.prep = nil
		<-p.done
		if p.f != nil {
			// Never published: a .tmp recovery deletes.
			abandon(p.f.Close())
		}
	}
	return err
}
