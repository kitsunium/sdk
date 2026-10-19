package consensus

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The shipped log (D4) is a directory of fixed-size, zero-filled segment
// files. Every record is 8-byte aligned:
//
//	length u32 | crc u32 | version u8 | kind u8 | end u8 | 0 u8 | batch u64 |
//	term u64 | index u64 | entry kind u8 | 0 0 0 | entry version u32 |
//	body sum u32 | chain u32 | payload | zero padding
//
// little-endian; length counts the bytes after the crc field, padding
// excluded, and the crc is the CRC-32C of those bytes. Records are written
// in batches: every record of a batch carries its sequence number, the last
// one the end-of-batch flag, and batch k+1 is written only after batch k was
// synced, so any valid record of batch k+1 proves batch k durable
// (R7-12, R8-01 to R8-04).
const (
	recHeader    int   = 8
	recFixed     int   = 44
	recVersion   uint8 = 1
	recEntry     uint8 = 1
	recHardState uint8 = 2
	recTruncate  uint8 = 3
	recCheckpt   uint8 = 4
	recSeal      uint8 = 5
	recCompact   uint8 = 6
	recCommit    uint8 = 7

	// u64Bytes is the width of every little-endian uint64 field written to
	// disk, in a record payload or in the stamp.
	u64Bytes int = 8
	// recAlign is the boundary every record starts on: recovery looks for a
	// valid record after a hole at each multiple of it, and nowhere else.
	recAlign int = 8
	// checkpointLen is a checkpoint record's payload: eight uint64s.
	checkpointLen int = 64
	// hardStateLen is a hard-state record's payload: term, vote and commit.
	hardStateLen int = 24

	// sealReserve keeps room for the seal record at a segment's end.
	sealReserve int64 = 64

	// DefaultSegmentBytes is a segment's fixed size (provisional).
	DefaultSegmentBytes int64 = 64 << 20
)

// align8 rounds n up to the next record boundary.
func align8(n int) int { return (n + 7) &^ 7 }

type record struct {
	kind   uint8
	end    bool
	batch  uint64
	term   uint64
	index  uint64
	ekind  uint8
	ever   uint32
	body   uint32
	chain  uint32
	data   []byte
	off    int64 // offset in its segment
	length int   // aligned length on disk
}

// appendRecord encodes r at the end of dst, crc sealed and zero-padded to
// the record boundary, and returns the extended slice.
func appendRecord(dst []byte, r *record) []byte {
	start := len(dst)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(recFixed+len(r.data)))
	dst = binary.LittleEndian.AppendUint32(dst, 0)
	dst = append(dst, recVersion, r.kind, boolByte(r.end), 0)
	dst = binary.LittleEndian.AppendUint64(dst, r.batch)
	dst = binary.LittleEndian.AppendUint64(dst, r.term)
	dst = binary.LittleEndian.AppendUint64(dst, r.index)
	dst = append(dst, r.ekind, 0, 0, 0)
	dst = binary.LittleEndian.AppendUint32(dst, r.ever)
	dst = binary.LittleEndian.AppendUint32(dst, r.body)
	dst = binary.LittleEndian.AppendUint32(dst, r.chain)
	dst = append(dst, r.data...)
	sealRecord(dst[start:])
	for (len(dst)-start)%8 != 0 {
		dst = append(dst, 0)
	}
	return dst
}

// boolByte is v as the record's flag byte.
func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// sealRecord recomputes a record's crc over its body.
func sealRecord(b []byte) {
	n := binary.LittleEndian.Uint32(b)
	binary.LittleEndian.PutUint32(b[4:], crc32.Checksum(b[recHeader:recHeader+int(n)], castagnoli))
}

// parseRecord decodes the record at b[0:]; ok is false for a hole: a short
// read, a zero header, a length out of range or a crc that fails.
func parseRecord(b []byte) (rec record, ok bool) {
	if len(b) < recHeader+recFixed {
		return rec, false
	}
	n := int(binary.LittleEndian.Uint32(b))
	if n < recFixed || recHeader+n > len(b) {
		return rec, false
	}
	body := b[recHeader : recHeader+n]
	if crc32.Checksum(body, castagnoli) != binary.LittleEndian.Uint32(b[4:]) || body[0] != recVersion {
		return rec, false
	}
	rec = record{
		kind: body[1], end: body[2] != 0,
		batch: binary.LittleEndian.Uint64(body[4:]), term: binary.LittleEndian.Uint64(body[12:]),
		index: binary.LittleEndian.Uint64(body[20:]), ekind: body[28],
		ever: binary.LittleEndian.Uint32(body[32:]), body: binary.LittleEndian.Uint32(body[36:]),
		chain: binary.LittleEndian.Uint32(body[40:]), data: body[recFixed:], length: align8(recHeader + n),
	}
	return rec, true
}

// checkpoint is the record that opens every segment: the hard state, the
// effective last entry before it, the compaction point and the next batch.
type checkpoint struct {
	hs           coreconsensus.HardState
	prevIndex    uint64
	prevTerm     uint64
	compact      uint64
	compactTerm  uint64
	nextBatchSeq uint64
}

// encode is the checkpoint record's payload: eight little-endian uint64s,
// in the order decodeCheckpoint reads them back.
func (c checkpoint) encode() []byte {
	b := make([]byte, 0, checkpointLen)
	b = binary.LittleEndian.AppendUint64(b, c.hs.Term)
	b = binary.LittleEndian.AppendUint64(b, uint64(c.hs.Vote))
	b = binary.LittleEndian.AppendUint64(b, c.hs.Commit)
	b = binary.LittleEndian.AppendUint64(b, c.prevIndex)
	b = binary.LittleEndian.AppendUint64(b, c.prevTerm)
	b = binary.LittleEndian.AppendUint64(b, c.compact)
	b = binary.LittleEndian.AppendUint64(b, c.compactTerm)
	return binary.LittleEndian.AppendUint64(b, c.nextBatchSeq)
}

// decodeCheckpoint parses encode's payload; ok is false for any other
// length, which recovery reads as a damaged checkpoint.
func decodeCheckpoint(b []byte) (checkpoint, bool) {
	if len(b) != checkpointLen {
		return checkpoint{}, false
	}
	u := func(i int) uint64 { return binary.LittleEndian.Uint64(b[8*i:]) }
	return checkpoint{
		hs:        coreconsensus.HardState{Term: u(0), Vote: coreconsensus.NodeID(u(1)), Commit: u(2)},
		prevIndex: u(3), prevTerm: u(4), compact: u(5), compactTerm: u(6), nextBatchSeq: u(7),
	}, true
}

// encodeHS is a hard-state record's payload.
func encodeHS(hs coreconsensus.HardState) []byte {
	b := binary.LittleEndian.AppendUint64(nil, hs.Term)
	b = binary.LittleEndian.AppendUint64(b, uint64(hs.Vote))
	return binary.LittleEndian.AppendUint64(b, hs.Commit)
}

// slot locates one retained entry: on disk once synced, in memory before.
type slot struct {
	seg  uint64
	off  int64
	term uint64
	mem  *coreconsensus.EntryValue
}

type segment struct {
	n    uint64
	f    *os.File
	size int64
}

// fileLog is the shipped segmented LogStore. One writer, many readers;
// appended entries are readable before they are synced (D4).
type fileLog struct {
	dir      string
	segBytes int64

	mu          sync.RWMutex
	segs        []*segment
	off         int64
	batch       uint64
	pending     []byte
	pendingLast int // offset in pending of the last record, -1 for none
	first       uint64
	slots       []slot
	prevTerm    uint64 // term of first-1
	hs          coreconsensus.HardState
	compact     uint64
	compactTerm uint64
	stamp       coreconsensus.StampValue
	// dur is the state the synced records describe: a rotation's checkpoint
	// is written from it, never from the pending batch it precedes.
	dur checkpoint
	// prep is the next segment, being prepared in the background.
	prep *prepared
	// tailsTruncated counts the torn tails recovery truncated.
	tailsTruncated int64
	// syncedAtOpen counts the segments recovery made durable before
	// trusting them; a test reads it.
	syncedAtOpen int
	failed       error
	refuse       error
	dd           *dataDir
	// runDir is the boot-scoped directory of the second STORAGE_FAILED
	// marker (R9-05). It is read once at open from runtimeMarkerDir and is
	// a field, not a constant, so a test can point it at a directory an
	// unprivileged process may create: /run is root's.
	runDir string
	// afterSeal runs once a rotation's seal is durable and before the next
	// segment is published. Only the dm-crypt lane sets it, to cut power in
	// that window; it is nil otherwise.
	afterSeal func()
	// duringSync runs between each write of a batch and its fdatasync, the
	// lock released: twice for a batch of several records. Only a test sets
	// it.
	duringSync func()
	// wmu serialises the writers. A sync holds it, not mu, across its
	// write and fdatasync, so readers do not wait for the disk (review of
	// PR #301, L6).
	wmu sync.Mutex
}

// segName is segment n's file name; fixed-width hex, so the names sort in
// segment order and recovery accepts no other spelling of n.
func segName(n uint64) string { return fmt.Sprintf("seg-%016x", n) }

// logDir is the directory holding the segments.
func (l *fileLog) logDir() string { return filepath.Join(l.dir, "log") }

// quarantined is the LOG_QUARANTINED of damage at off in segment seg.
func quarantined(seg uint64, off int64, why string) error {
	return origin(coreconsensus.LogQuarantined, errs.String("segment", segName(seg)),
		errs.Int64("offset", off), errs.String("why", why))
}

// openFileLog opens, and recovers, the log of a data directory. A damaged
// log is quarantined: the marker is written FIRST and nothing is modified
// (D4).
func openFileLog(dd *dataDir, segBytes int64) (*fileLog, error) {
	if segBytes <= 0 {
		segBytes = DefaultSegmentBytes
	}
	l := &fileLog{dir: dd.dir, segBytes: segBytes, dd: dd, first: 1, pendingLast: -1, runDir: runtimeMarkerDir()}
	if err := os.MkdirAll(l.logDir(), 0o700); err != nil {
		return nil, storageFailed("mkdir", err)
	}
	st, err := readStamp(dd.dir)
	if err != nil {
		return nil, err
	}
	l.stamp = st
	if q, err := os.ReadFile(filepath.Join(dd.dir, markerQuarantine)); err == nil {
		l.refuse = origin(coreconsensus.LogQuarantined, errs.String("marker", strings.TrimSpace(string(q))))
		return l, nil
	}
	if err := l.recover(); err != nil {
		if errs.HasCode(err, coreconsensus.CodeLogQuarantined) {
			l.refuse = err
			return l, nil
		}
		return nil, err
	}
	return l, nil
}

// scanned is one segment as recovery read it, before anything is changed.
type scanned struct {
	n       uint64
	data    []byte
	recs    []record
	hole    int64 // -1 when none
	sealed  bool
	trimmed int64 // truncation point decided for the last segment, -1 none
}

// recover runs the written recovery procedure (D4, R8-02, R9-01, R9-03):
// scan every segment, classify every hole, and only then change anything.
func (l *fileLog) recover() error {
	nums, err := l.segmentNumbers()
	if err != nil {
		return err
	}
	if len(nums) == 0 {
		return l.createSegment(1, &checkpoint{nextBatchSeq: 1})
	}
	segs, err := l.scanSegments(nums)
	if err != nil {
		return err
	}
	if err := l.checkBatchSequence(segs); err != nil {
		return err
	}
	last := segs[len(segs)-1]
	if err := l.trimTornTail(last); err != nil {
		return err
	}
	if err := l.replay(segs); err != nil {
		return err
	}
	l.applyCompaction()
	if err := l.openSegments(segs); err != nil {
		return err
	}
	if err := l.syncAtOpen(); err != nil {
		return err
	}
	tail := l.segs[len(l.segs)-1]
	if err := l.positionTail(last, tail); err != nil {
		return err
	}
	l.batch++
	l.markDurable()
	if last.sealed {
		// Crash after the seal and before the next segment's rename: the
		// seal is durable, so only the publication is left. Sealing again
		// would spend a batch number no reader sees (review H2).
		return l.publishNext(0)
	}
	l.prepare(tail.n+1, l.segBytes)
	return nil
}

// segmentNumbers lists the published segments, sorted, deleting on the way
// every .tmp: only an INCOMPLETE .tmp exists, one that never reached its
// rename (R2-05). A name that is not exactly a segment's is ignored.
func (l *fileLog) segmentNumbers() ([]uint64, error) {
	names, err := os.ReadDir(l.logDir())
	if err != nil {
		return nil, storageFailed("readdir", err)
	}
	var nums []uint64
	for _, e := range names {
		name := e.Name()
		if strings.HasSuffix(name, ".tmp") {
			if err := os.Remove(filepath.Join(l.logDir(), name)); err != nil {
				return nil, storageFailed("remove tmp", err)
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

// scanSegments reads and parses every segment in order. A segment is judged
// as soon as it is read, so the first damage found is the one quarantined,
// and nothing after it is read.
func (l *fileLog) scanSegments(nums []uint64) ([]*scanned, error) {
	segs := make([]*scanned, len(nums))
	for i, n := range nums {
		s, err := l.scanSegment(n)
		if err != nil {
			return nil, err
		}
		segs[i] = s
		if len(s.recs) == 0 || s.recs[0].kind != recCheckpt {
			// A published segment whose checkpoint is invalid is corruption,
			// not an interrupted rotation (R3-08).
			return nil, l.quarantine(n, 0, "invalid checkpoint")
		}
		if i < len(nums)-1 && !s.sealed {
			return nil, l.quarantine(n, s.hole, "hole in a sealed segment")
		}
	}
	return segs, nil
}

// scanSegment reads segment n and parses its records up to the first hole,
// or through its seal: a sealed segment is read up to its sealed length
// (R9-02).
func (l *fileLog) scanSegment(n uint64) (*scanned, error) {
	b, err := os.ReadFile(filepath.Join(l.logDir(), segName(n)))
	if err != nil {
		return nil, storageFailed("read segment", err)
	}
	s := &scanned{n: n, data: b, hole: -1, trimmed: -1}
	limit := int64(len(b))
	var off int64
	for off < limit {
		r, ok := parseRecord(b[off:limit])
		if !ok {
			s.hole = off
			break
		}
		r.off = off
		s.recs = append(s.recs, r)
		off += int64(r.length)
		if r.kind == recSeal {
			s.sealed = true
			break
		}
	}
	return s, nil
}

// checkBatchSequence quarantines a log whose batch sequences are not
// contiguous across the whole log (R9-01).
func (l *fileLog) checkBatchSequence(segs []*scanned) error {
	var prev *record
	for _, s := range segs {
		for i := range s.recs {
			r := &s.recs[i]
			if prev != nil && !continuesBatch(prev, r) {
				return l.quarantine(s.n, r.off, "batch sequence")
			}
			prev = r
		}
	}
	return nil
}

// continuesBatch reports whether r may follow prev: in prev's batch, or
// first of the next one once prev ended its own.
func continuesBatch(prev, r *record) bool {
	return r.batch == prev.batch || (r.batch == prev.batch+1 && prev.end)
}

// trimTornTail decides the fate of a hole in the last segment: a torn tail
// is dropped from the records, its truncation point kept for positionTail
// to zero and sync before any new record (R8-03); any other hole is
// quarantined.
func (l *fileLog) trimTornTail(last *scanned) error {
	if last.hole < 0 {
		return nil
	}
	trim, why := classifyTail(last.data, last.recs, last.hole)
	if trim < 0 {
		return l.quarantine(last.n, last.hole, why)
	}
	k := len(last.recs)
	for k > 0 && last.recs[k-1].off >= trim {
		k--
	}
	last.recs = last.recs[:k]
	last.trimmed = trim
	l.tailsTruncated++
	return nil
}

// replay builds the effective log: it applies every segment's checkpoint
// and records in order, truncate records included, and judges continuity
// on the result (R8-01).
func (l *fileLog) replay(segs []*scanned) error {
	for si, s := range segs {
		cp, ok := decodeCheckpoint(s.recs[0].data)
		if !ok {
			return l.quarantine(s.n, 0, "checkpoint payload")
		}
		if err := l.applyCheckpoint(si == 0, s.n, &cp); err != nil {
			return err
		}
		for _, r := range s.recs[1:] {
			if err := l.applyRecord(s.n, &r); err != nil {
				return err
			}
			l.batch = r.batch
		}
		if s.recs[0].batch > l.batch {
			l.batch = s.recs[0].batch
		}
	}
	return nil
}

// applyCheckpoint applies the checkpoint opening segment seg. The first
// segment's sets the log's start; every later one must continue the log,
// and as it was written at activation with the CURRENT hard state, it is
// the newest when read in order.
func (l *fileLog) applyCheckpoint(first bool, seg uint64, cp *checkpoint) error {
	if first {
		l.hs = cp.hs
		l.compact, l.compactTerm = cp.compact, cp.compactTerm
		l.first = cp.prevIndex + 1
		l.prevTerm = cp.prevTerm
		return nil
	}
	if want := l.first + uint64(len(l.slots)) - 1; cp.prevIndex != want {
		return l.quarantine(seg, 0, "checkpoint does not continue the log")
	}
	l.hs = cp.hs
	if cp.compact > l.compact {
		l.compact, l.compactTerm = cp.compact, cp.compactTerm
	}
	return nil
}

// applyRecord applies one record of segment seg to the log being rebuilt.
// Only an entry out of sequence is refused; a record kind recovery does not
// rebuild from changes nothing.
func (l *fileLog) applyRecord(seg uint64, r *record) error {
	switch r.kind {
	case recEntry:
		if r.index != l.first+uint64(len(l.slots)) {
			return l.quarantine(seg, r.off, "index out of sequence")
		}
		l.slots = append(l.slots, slot{seg: seg, off: r.off, term: r.term})
	case recTruncate:
		l.applyTruncate(r.index)
	case recHardState:
		if len(r.data) == hardStateLen {
			l.hs = decodeHS(r.data)
		}
	case recCommit:
		l.hs.Commit = max(l.hs.Commit, r.index)
	case recCompact:
		l.compact, l.compactTerm = max(l.compact, r.index), r.term
	}
	return nil
}

// applyTruncate applies a truncate record at index. One that truncates into
// a dropped segment removes entries that are gone with it, and a later
// compact record covers what follows.
func (l *fileLog) applyTruncate(index uint64) {
	switch {
	case index >= l.first && index <= l.first+uint64(len(l.slots)):
		l.slots = l.slots[:index-l.first]
	case index < l.first:
		l.first, l.slots, l.prevTerm = index, nil, 0
	}
}

// decodeHS parses encodeHS's payload; the caller checked its length.
func decodeHS(b []byte) coreconsensus.HardState {
	return coreconsensus.HardState{
		Term: binary.LittleEndian.Uint64(b),
		Vote: coreconsensus.NodeID(binary.LittleEndian.Uint64(b[8:])), Commit: binary.LittleEndian.Uint64(b[16:]),
	}
}

// applyCompaction drops the retained entries the compaction point covers,
// keeping the term of the last one dropped as the term before the log.
func (l *fileLog) applyCompaction() {
	if l.compact < l.first {
		return
	}
	drop := min(l.compact-l.first+1, uint64(len(l.slots)))
	if drop > 0 {
		l.prevTerm = l.slots[drop-1].term
	}
	l.slots = l.slots[drop:]
	l.first = l.compact + 1
	if drop == 0 {
		l.prevTerm = l.compactTerm
	}
}

// openSegments opens every scanned segment for reading and writing, in
// order. A segment that fails to open is a STORAGE_FAILED; those already
// opened stay in l.segs for Close.
func (l *fileLog) openSegments(segs []*scanned) error {
	for _, s := range segs {
		f, err := os.OpenFile(filepath.Join(l.logDir(), segName(s.n)), os.O_RDWR, 0)
		if err != nil {
			return storageFailed("open segment", err)
		}
		l.segs = append(l.segs, &segment{n: s.n, f: f, size: int64(len(s.data))})
	}
	return nil
}

// syncAtOpen makes durable everything recovery is about to trust, before
// anything is acknowledged from it. A process killed between a batch's write
// and its fdatasync leaves that batch in the page cache, complete with its
// end record: a restart in the same boot reads it as written, and would
// acknowledge entries no device holds, which a power cut within the
// write-back delay then loses. The same holds for a rename or an unlink
// whose directory sync never ran. PostgreSQL syncs its data directory before
// crash recovery for this reason (SyncDataDirectory). A write-back that
// failed after the process died is reported to this fresh descriptor too
// (errseq, Linux 4.13), so a failure here is STORAGE_FAILED, never a log
// trusted from pages the disk refused.
func (l *fileLog) syncAtOpen() error {
	for _, s := range l.segs {
		if err := datasync(s.f); err != nil {
			return storageFailed("sync at open", err)
		}
		l.syncedAtOpen++
	}
	for _, dir := range []string{l.logDir(), l.dir} {
		if err := syncDir(dir); err != nil {
			return storageFailed("sync at open", err)
		}
	}
	return nil
}

// positionTail sets the write offset in the last segment: at the torn
// tail's truncation point, zeroing and syncing what lies past it unless it
// is zero already, else right after the last valid record.
func (l *fileLog) positionTail(last *scanned, tail *segment) error {
	switch {
	case last.trimmed >= 0:
		l.off = last.trimmed
		if !allZero(last.data[l.off:]) {
			if err := zeroFrom(tail.f, l.off, tail.size); err != nil {
				return storageFailed("zero tail", err)
			}
		}
	case len(last.recs) > 0:
		lr := last.recs[len(last.recs)-1]
		l.off = lr.off + int64(lr.length)
	}
	return nil
}

// classifyTail is step 2 to 4 of the decision procedure (R8-02, R9-01,
// R9-03): it returns where to truncate a torn tail, or -1 and why the hole
// is quarantined.
func classifyTail(b []byte, recs []record, hole int64) (int64, string) {
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
			r.off = int64(off)
			after = append(after, r)
			off += r.length - recAlign
		}
	}
	return after
}

// judgeAfterHole returns why a hole is quarantined given the valid records
// after it, or "" when they do not forbid a tear.
func judgeAfterHole(after []record, torn uint64) string {
	for _, r := range after {
		if r.batch != torn {
			// A valid record of another batch after a hole: never a tear
			// (R9-01).
			return "valid record of another batch after a hole"
		}
		if r.end {
			// The torn batch's end record IS valid: it may have been
			// acknowledged, and damage inside it is quarantined (R9-03).
			return "damage inside a batch whose end record is valid"
		}
	}
	return ""
}

// tornBatchStart is where the batch torn, which never completed, begins:
// all of it is dropped, as it was never synced and never acknowledged. A
// torn batch that starts with the segment's checkpoint is quarantined.
func tornBatchStart(recs []record, lastOff int64, torn uint64) (int64, string) {
	start := lastOff
	for i := len(recs) - 1; i >= 0 && recs[i].batch == torn; i-- {
		start = recs[i].off
	}
	if recs[0].batch == torn && start == recs[0].off {
		return -1, "torn checkpoint"
	}
	return start, ""
}

// allZero reports whether b holds only zero bytes.
func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// zeroFrom zeroes f from off to size, 1 MiB at a time, and syncs it; the
// sync runs even when there is nothing to zero, so the caller may rely on
// the range being durable zeros on return.
func zeroFrom(f *os.File, off, size int64) error {
	if off >= size {
		return datasync(f)
	}
	zero := make([]byte, min(size-off, 1<<20))
	for p := off; p < size; {
		n := min(int64(len(zero)), size-p)
		if _, err := f.WriteAt(zero[:n], p); err != nil {
			return err
		}
		p += n
	}
	return datasync(f)
}

// quarantine writes and syncs the marker FIRST and modifies nothing else.
func (l *fileLog) quarantine(seg uint64, off int64, why string) error {
	qerr := quarantined(seg, off, why)
	line := fmt.Sprintf("%s %d %s\n", segName(seg), off, why)
	if err := writeFileSync(filepath.Join(l.dir, markerQuarantine), []byte(line)); err != nil {
		return storageFailed("quarantine marker", err)
	}
	return qerr
}

// createSegment publishes segment n: zero-filled tmp, checkpoint, fsync,
// rename, fsync of the directory (D4 lifecycle steps 1 to 3).
func (l *fileLog) createSegment(n uint64, cp *checkpoint) error {
	size := l.segBytes
	cpRec := appendRecord(nil, &record{kind: recCheckpt, end: true, batch: cp.nextBatchSeq, data: cp.encode()})
	tmp := filepath.Join(l.logDir(), segName(n)+".tmp")
	f, err := l.segmentFile(n, size, tmp)
	if err != nil {
		return err
	}
	// The checkpoint is written at ACTIVATION, with the current hard state,
	// never the state of the moment the segment was prepared (R3-24, R4-01).
	if _, err := f.WriteAt(cpRec, 0); err != nil {
		return abandonTmp(f, storageFailed("checkpoint", err))
	}
	if err := f.Sync(); err != nil {
		return abandonTmp(f, storageFailed("sync segment", err))
	}
	// Closed before the rename and opened again after: Windows refuses to
	// rename a file a handle holds open.
	if err := f.Close(); err != nil {
		return storageFailed("close segment", err)
	}
	final := filepath.Join(l.logDir(), segName(n))
	if err := os.Rename(tmp, final); err != nil {
		return storageFailed("rename segment", err)
	}
	if err := syncDir(l.logDir()); err != nil {
		return storageFailed("sync dir", err)
	}
	f, err = os.OpenFile(final, os.O_RDWR, 0)
	if err != nil {
		return storageFailed("open segment", err)
	}
	l.segs = append(l.segs, &segment{n: n, f: f, size: size})
	l.off = int64(len(cpRec))
	l.batch = cp.nextBatchSeq + 1
	l.prepare(n+1, l.segBytes)
	return nil
}

// segmentFile is the zero-filled .tmp of segment n at size: the prepared
// one when it is that segment at that size, else one created and zeroed now,
// on the writer's path, where a failure stops the log.
func (l *fileLog) segmentFile(n uint64, size int64, tmp string) (*os.File, error) {
	if f := l.takePrepared(n, size); f != nil {
		return f, nil
	}
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, storageFailed("create segment", err)
	}
	if err := zeroFrom(f, 0, size); err != nil {
		return nil, abandonTmp(f, storageFailed("zero-fill", err))
	}
	return f, nil
}

// abandonTmp closes f, a segment .tmp a failure leaves unpublished, and
// returns cause, the failure that stops the log. The .tmp is never renamed
// and recovery deletes it, so what its Close says changes nothing.
func abandonTmp(f io.Closer, cause error) error {
	abandon(f.Close())
	return cause
}

// prepared is the next segment, zero-filled in the background as a .tmp
// while the current one fills (R3-24, R4-01). It is never published early:
// recovery deletes any .tmp, and its checkpoint is written at activation.
type prepared struct {
	n    uint64
	size int64
	done chan struct{}
	f    *os.File
	err  error
}

// prepare launches the background goroutine that zero-fills segment n off
// the writer's path. The goroutine ends on its own once the .tmp is zeroed
// or has failed, closing done as it returns: done is created here and
// closed by that goroutine alone, exactly once, and takePrepared and Close
// only ever wait on it.
func (l *fileLog) prepare(n uint64, size int64) {
	done := make(chan struct{})
	p := &prepared{n: n, size: size, done: done}
	l.prep = p
	go func() {
		defer close(done)
		tmp := filepath.Join(l.logDir(), segName(n)+".tmp")
		f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			p.err = err
			return
		}
		if err := zeroFrom(f, 0, size); err != nil {
			// A half-zeroed .tmp is never published: takePrepared then
			// builds the segment on the writer's path, where a failure
			// stops the node, and recovery deletes the .tmp.
			abandon(f.Close())
			p.err = err
			return
		}
		p.f = f
	}()
}

// takePrepared waits for the prepared segment n, when it is the one wanted
// at that size; nil means prepare it now.
func (l *fileLog) takePrepared(n uint64, size int64) *os.File {
	p := l.prep
	l.prep = nil
	if p == nil {
		return nil
	}
	<-p.done
	if p.err != nil || p.n != n || p.size != size {
		if p.f != nil {
			// The wrong segment, or the wrong size: an unpublished .tmp
			// nothing will read, which recovery deletes.
			abandon(p.f.Close())
		}
		return nil
	}
	return p.f
}

// writeFileSync publishes data at path atomically and durably: written and
// synced to path.tmp, renamed over path, and the directory synced.
func writeFileSync(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := writeSynced(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// writeSynced writes data to a fresh file at path and syncs it. The file is
// closed before it returns, so before writeFileSync renames it, and a Close
// failure is reported like the sync's: it is part of the write.
func writeSynced(path string, data []byte) (err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// rotate seals the current segment in its own synced batch, then publishes
// the next one with the CURRENT hard state. It runs between batches only
// (R8-04). need is the size of the batch that did not fit.
func (l *fileLog) rotate(need int) error {
	cur := l.segs[len(l.segs)-1]
	seal := appendRecord(nil, &record{kind: recSeal, end: true, batch: l.batch, index: uint64(l.off)})
	if l.off+int64(len(seal)) <= cur.size {
		if _, err := cur.f.WriteAt(seal, l.off); err != nil {
			return storageFailed("seal", err)
		}
		if err := datasync(cur.f); err != nil {
			return storageFailed("sync seal", err)
		}
		l.batch++
		if l.afterSeal != nil {
			l.afterSeal()
		}
	}
	return l.publishNext(need)
}

// publishNext publishes the segment after the current one, whose seal is
// durable, with the CURRENT hard state and the next batch number.
func (l *fileLog) publishNext(need int) error {
	cur := l.segs[len(l.segs)-1]
	cp := l.dur
	cp.nextBatchSeq = l.batch
	saved := l.segBytes
	if int64(need)+1024+sealReserve > l.segBytes {
		l.segBytes = int64(align8(need)) + 1024 + sealReserve
	}
	err := l.createSegment(cur.n+1, &cp)
	l.segBytes = saved
	return err
}

// termLocked is the term of entry i, the term before the log for first-1,
// and 0 outside the log; the caller holds mu.
func (l *fileLog) termLocked(i uint64) uint64 {
	switch {
	case i == l.first-1:
		return l.prevTerm
	case i < l.first || i >= l.first+uint64(len(l.slots)):
		return 0
	}
	return l.slots[i-l.first].term
}

// usable is why the log refuses a call: its quarantine first, then the
// failure that stopped it, nil while it is healthy.
func (l *fileLog) usable() error {
	if l.refuse != nil {
		return l.refuse
	}
	return l.failed
}

// ---- LogReader ----

// Entries implements LogReader.
func (l *fileLog) Entries(lo, hi uint64, maxBytes int, dst []coreconsensus.EntryValue) ([]coreconsensus.EntryValue, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if err := l.usable(); err != nil {
		return dst, err
	}
	if lo < l.first {
		return dst, compacted(lo, l.first)
	}
	if hi > l.first+uint64(len(l.slots)) {
		return dst, storageFailed("entries", errImpossible)
	}
	size := 0
	for i := lo; i < hi; i++ {
		s := l.slots[i-l.first]
		var e coreconsensus.EntryValue
		if s.mem != nil {
			e = *s.mem
		} else {
			var err error
			if e, err = l.readEntry(s); err != nil {
				return dst, err
			}
		}
		size += len(e.Data)
		if i > lo && size > maxBytes {
			break
		}
		dst = append(dst, e)
	}
	return dst, nil
}

// readEntry reads the synced entry s locates from its segment. A record
// that does not parse as an entry there is LOG_CORRUPT: the log disagrees
// with what it recorded.
func (l *fileLog) readEntry(s slot) (coreconsensus.EntryValue, error) {
	var seg *segment
	for _, x := range l.segs {
		if x.n == s.seg {
			seg = x
		}
	}
	if seg == nil {
		return coreconsensus.EntryValue{}, storageFailed("read", errImpossible)
	}
	var h [recHeader]byte
	if _, err := seg.f.ReadAt(h[:], s.off); err != nil {
		return coreconsensus.EntryValue{}, storageFailed("read", err)
	}
	// Bounded by the segment before the conversion: a damaged length must
	// not turn negative on 32 bits nor allocate past the file (#422).
	ln := binary.LittleEndian.Uint32(h[:])
	if int64(ln) > seg.size-s.off-int64(recHeader) {
		return coreconsensus.EntryValue{}, origin(coreconsensus.LogCorrupt, errs.String("segment", segName(s.seg)), errs.Int64("offset", s.off))
	}
	b := make([]byte, recHeader+int(ln))
	if _, err := seg.f.ReadAt(b, s.off); err != nil && !errors.Is(err, io.EOF) {
		return coreconsensus.EntryValue{}, storageFailed("read", err)
	}
	r, ok := parseRecord(b)
	if !ok || r.kind != recEntry {
		// A log that cannot be read cannot be trusted to agree with itself.
		return coreconsensus.EntryValue{}, origin(coreconsensus.LogCorrupt, errs.String("segment", segName(s.seg)), errs.Int64("offset", s.off))
	}
	return coreconsensus.EntryValue{
		Position: coreconsensus.PositionValue{Term: r.term, Index: r.index},
		Kind:     coreconsensus.EntryKind(r.ekind), Version: r.ever, BodySum: r.body, ChainSum: r.chain, Data: r.data,
	}, nil
}

// Term implements LogReader.
func (l *fileLog) Term(index uint64) (uint64, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	switch {
	case index == l.first-1:
		return l.prevTerm, nil
	case index < l.first:
		return 0, compacted(index, l.first)
	case index >= l.first+uint64(len(l.slots)):
		return 0, storageFailed("term", errImpossible)
	}
	return l.slots[index-l.first].term, nil
}

// Bounds implements LogReader; a refused log is empty.
func (l *fileLog) Bounds() (first, last uint64) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.refuse != nil {
		return 1, 0
	}
	return l.first, l.first + uint64(len(l.slots)) - 1
}

// ---- LogStore ----

// add appends r to the pending batch and remembers it as the batch's last
// record, the one Sync flags end-of-batch.
func (l *fileLog) add(r *record) {
	l.pendingLast = len(l.pending)
	l.pending = appendRecord(l.pending, r)
}

// Append implements LogStore: entries and the hard state in ONE batch.
func (l *fileLog) Append(entries []coreconsensus.EntryValue, hs *coreconsensus.HardState) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return err
	}
	for i := range entries {
		e := entries[i]
		if e.Position.Index != l.first+uint64(len(l.slots)) {
			return storageFailed("append", errImpossible)
		}
		l.add(&record{
			kind: recEntry, batch: l.batch, term: e.Position.Term, index: e.Position.Index,
			ekind: uint8(e.Kind), ever: e.Version, body: e.BodySum, chain: e.ChainSum, data: e.Data,
		})
		l.slots = append(l.slots, slot{term: e.Position.Term, mem: &e})
	}
	if hs != nil {
		l.add(&record{kind: recHardState, batch: l.batch, data: encodeHS(*hs)})
		l.hs = *hs
	}
	return nil
}

// Sync implements LogStore: the pending batch is written, its last record
// flagged end-of-batch, and made durable by two fdatasyncs, the end record
// written only once the rest is durable (writeBatch). A failure is final:
// the log refuses every later call (D4, fsyncgate).
func (l *fileLog) Sync() error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
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
		// Rotation happens only between batches (R8-04): restamp the batch
		// with the sequence the new segment expects.
		if err := l.rotate(len(l.pending)); err != nil {
			l.failed = err
			return err
		}
		l.restamp(l.batch)
		cur = l.segs[len(l.segs)-1]
	}
	// The writes and their fdatasync run without mu: the writers are held
	// by wmu, readers find the pending entries in memory and the durable
	// ones below l.off, which the writes do not touch.
	l.mu.Unlock()
	ferr := l.writeBatch(cur)
	l.mu.Lock()
	if ferr != nil {
		l.failed = ferr
		return l.failed
	}
	l.settleBatch(cur.n)
	return l.dropSegmentsLocked()
}

// writeBatch makes the pending batch durable at l.off in segment cur in two
// steps: every record before the last, synced, then the last one, which
// carries the end-of-batch flag, synced (D16, #417). A power cut during one
// fdatasync may persist any subset of its dirty pages, the end record's
// without an earlier one's; with one sync per batch that left a valid end
// record after a hole in a batch never acknowledged, which recovery must
// quarantine. Written last and on its own, a valid end record proves that
// the rest of its batch was durable before it, so a hole before it is
// damage to synced data, and nothing else.
func (l *fileLog) writeBatch(cur *segment) error {
	body := l.pending[:l.pendingLast]
	if len(body) > 0 {
		if err := l.writeAndSync(cur, body, l.off); err != nil {
			return err
		}
	}
	return l.writeAndSync(cur, l.pending[l.pendingLast:], l.off+int64(len(body)))
}

// writeAndSync writes b at off in segment cur and fdatasyncs it.
func (l *fileLog) writeAndSync(cur *segment, b []byte, off int64) error {
	if _, err := cur.f.WriteAt(b, off); err != nil {
		return storageFailed("write", err)
	}
	if l.duringSync != nil {
		l.duringSync()
	}
	if err := datasync(cur.f); err != nil {
		return storageFailed("fdatasync", err)
	}
	return nil
}

// settleBatch records the pending batch, now synced in segment seg, as
// durable: its entries are read from disk from here on, the write offset
// moves past it, and the next batch gets the next sequence number. The
// caller holds mu.
func (l *fileLog) settleBatch(seg uint64) {
	l.relocate(seg)
	l.off += int64(len(l.pending))
	l.markDurable()
	l.pending = l.pending[:0]
	l.pendingLast = -1
	l.batch++
}

// relocate points every slot the pending batch carried at its record in
// segment seg, now that the batch is synced there, and releases its
// in-memory copy. A slot replaced since, by another term, is left alone.
func (l *fileLog) relocate(seg uint64) {
	var off int64
	for off < int64(len(l.pending)) {
		r, _ := parseRecord(l.pending[off:])
		if r.kind == recEntry && r.index >= l.first && r.index < l.first+uint64(len(l.slots)) {
			s := &l.slots[r.index-l.first]
			if s.mem != nil && s.term == r.term {
				s.seg, s.off, s.mem = seg, l.off+off, nil
			}
		}
		off += int64(r.length)
	}
}

// markDurable records the state the synced records now describe, which a
// rotation's checkpoint is written from.
func (l *fileLog) markDurable() {
	last := l.first + uint64(len(l.slots)) - 1
	l.dur = checkpoint{hs: l.hs, prevIndex: last, prevTerm: l.termLocked(last), compact: l.compact, compactTerm: l.compactTerm}
}

// restamp rewrites the batch sequence of every pending record.
func (l *fileLog) restamp(batch uint64) {
	var off int
	for off < len(l.pending) {
		binary.LittleEndian.PutUint64(l.pending[off+recHeader+4:], batch)
		sealRecord(l.pending[off:])
		n := int(binary.LittleEndian.Uint32(l.pending[off:]))
		off += align8(recHeader + n)
	}
}

// TruncateSuffix implements LogStore: ONE truncate record, nothing else
// (R7-12, R8-01, R8-03).
func (l *fileLog) TruncateSuffix(from uint64) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return err
	}
	if from < l.first {
		from = l.first
	}
	if from >= l.first+uint64(len(l.slots)) {
		return nil
	}
	l.add(&record{kind: recTruncate, batch: l.batch, index: from})
	l.slots = l.slots[:from-l.first]
	return nil
}

// CompactPrefix implements LogStore: a compact record, then whole segments
// whose entries the next checkpoint already supersedes are deleted, as a
// prefix, with a directory fsync after the unlinks (D4 step 6). Past the end
// of the log there is no entry to read the compaction point's term from, and
// a guessed one is a log-matching check answered falsely: that compaction is
// refused, and the engine makes it with compactPrefixAt (#415).
func (l *fileLog) CompactPrefix(through uint64) error { return l.compactPrefixAt(through, 0) }

// compactPrefixAt is CompactPrefix with the term of the compaction point as
// the caller knows it, 0 when it does not. The log's own term wins where it
// holds the entry, and must agree with a known one; past its end the
// caller's term is recorded, and an unknown one refuses the compaction.
func (l *fileLog) compactPrefixAt(through, term uint64) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return err
	}
	if through < l.first {
		return nil
	}
	last := l.first + uint64(len(l.slots)) - 1
	t, err := compactionTerm(through, last, l.termLocked(through), term)
	if err != nil {
		return err
	}
	l.add(&record{kind: recCompact, batch: l.batch, index: through, term: t})
	if through >= last {
		l.slots = nil
	} else {
		l.slots = slices.Clone(l.slots[through-l.first+1:])
	}
	l.first, l.prevTerm = through+1, t
	l.compact, l.compactTerm = through, t
	return nil
}

// compactionTerm is the term a compaction through index through records,
// over a log whose last index is last and which answers held for that
// index: the log's own where it holds the entry, the caller's past its end.
// A caller's term that disagrees with the log's, or none past the end, is
// refused: the term recorded there is what a node with no table votes on.
func compactionTerm(through, last, held, given uint64) (uint64, error) {
	switch {
	case through <= last && given != 0 && given != held:
		return 0, storageFailed("compact", errImpossible)
	case through <= last:
		return held, nil
	case given == 0:
		return 0, storageFailed("compact", errUnknownCompactTerm)
	}
	return given, nil
}

// dropSegmentsLocked deletes the prefix of sealed segments whose every
// entry is at or below the compaction point. It runs after a sync, so the
// compact record and the next checkpoint are durable first.
func (l *fileLog) dropSegmentsLocked() error {
	removed := false
	for len(l.segs) > 1 {
		superseded, err := l.supersedes(l.segs[1])
		if err != nil {
			return err
		}
		if !superseded {
			break
		}
		if l.retainsEntryIn(l.segs[0].n) {
			// Stop here, but through the sync below: a segment this pass
			// already unlinked is published only once its directory is
			// synced (D4 lifecycle step 6).
			break
		}
		if err := l.removeFirstSegment(); err != nil {
			return err
		}
		removed = true
	}
	if removed {
		return syncDir(l.logDir())
	}
	return nil
}

// supersedes reports whether next's checkpoint starts at or below the
// compaction point, so the segment before it holds nothing the log still
// needs. A checkpoint that does not parse supersedes nothing.
func (l *fileLog) supersedes(next *segment) (bool, error) {
	b := make([]byte, recHeader+recFixed+checkpointLen)
	if _, err := next.f.ReadAt(b, 0); err != nil {
		return false, storageFailed("read checkpoint", err)
	}
	r, ok := parseRecord(b)
	if !ok {
		return false, nil
	}
	cp, ok := decodeCheckpoint(r.data)
	return ok && cp.prevIndex <= l.compact, nil
}

// retainsEntryIn reports whether a retained entry still lives in segment
// seg.
func (l *fileLog) retainsEntryIn(seg uint64) bool {
	for _, s := range l.slots {
		if s.seg == seg {
			return true
		}
	}
	return false
}

// removeFirstSegment closes and unlinks the oldest segment; the caller
// syncs the directory once every removal is done.
func (l *fileLog) removeFirstSegment() error {
	if err := l.segs[0].f.Close(); err != nil {
		return storageFailed("close segment", err)
	}
	if err := os.Remove(filepath.Join(l.logDir(), segName(l.segs[0].n))); err != nil {
		return storageFailed("remove segment", err)
	}
	l.segs = l.segs[1:]
	return nil
}

// HardState implements LogStore.
func (l *fileLog) HardState() (coreconsensus.HardState, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.hs, l.usable()
}

// Stamp implements LogStore.
func (l *fileLog) Stamp() (coreconsensus.StampValue, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.stamp, nil
}

// WriteStamp implements LogStore: durable on return.
func (l *fileLog) WriteStamp(s coreconsensus.StampValue) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := writeStamp(l.dir, s); err != nil {
		return storageFailed("stamp", err)
	}
	l.stamp = s
	return nil
}

// Close implements LogStore; the DataDir closes it. Every segment is closed
// whatever the one before it answered, and the first failure is reported: a
// Close can be where a deferred write-back error surfaces, and a log that
// swallowed it would report a clean shutdown of a disk that failed.
func (l *fileLog) Close() error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	var err error
	for _, s := range l.segs {
		if cerr := s.f.Close(); cerr != nil && err == nil {
			err = storageFailed("close segment", cerr)
		}
	}
	l.segs = nil
	if p := l.prep; p != nil {
		l.prep = nil
		<-p.done
		if p.f != nil {
			// The prepared segment was never published: a .tmp that
			// recovery deletes.
			abandon(p.f.Close())
		}
	}
	return err
}

// logStats reports the bytes the segments occupy, the free bytes of their
// filesystem, and the torn tails truncated at open, for D13's gauges.
func (l *fileLog) logStats() (bytes, free, tails int64) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, s := range l.segs {
		bytes += s.size
	}
	return bytes, fsFree(l.dir), l.tailsTruncated
}
