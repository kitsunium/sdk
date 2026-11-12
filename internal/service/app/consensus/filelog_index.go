package consensus

import (
	"slices"
	"sort"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// The file log's resident index (#1111). It used to keep one 40-byte slot
// per retained entry — segment, offset, term, length and a pointer to the
// entry still in memory —, about a tenth of the retained log in heap with
// 256-byte entries, outside every bound of MemoryBudget. It is three parts
// now:
//
//   - a table per segment of the offsets of its retained entries, in units
//     of recAlign, 4 bytes an entry, with the index of its first one and an
//     upper bound of its last record's end;
//   - the term runs, one {first index, term} per change of term, which an
//     election alone makes;
//   - the entries of the batch not synced yet, in memory, the log's tail.
//
// Every retained entry lies after the one before it, in the same segment or
// a later one: a write is appended, a truncation drops a suffix and the
// entries written again after it come later still. So the tables are in
// index order, and so are the offsets of one table.

// indexEntryBytes is what the index keeps resident for one synced entry: its
// offset in its segment's table. MemoryBudget counts it for every record
// MaxLogBytes can hold.
const indexEntryBytes int64 = 4

// minRecordBytes is the smallest record an entry takes on disk, an empty
// payload's: a log of n bytes holds at most n / minRecordBytes entries.
const minRecordBytes int64 = int64((recHeader + recFixed + recAlign - 1) &^ (recAlign - 1))

// maxIndexedOffset is the first offset a table cannot hold: 2^32 units of
// recAlign, 32 GiB, far past any segment the log writes.
const maxIndexedOffset int64 = 1 << 32 * int64(recAlign)

// segIndex locates the retained entries one segment holds, from index first
// on: offs[k] is entry first+k's record offset over recAlign, and end bounds
// the last one's record from above, its exact end once appended, the offset
// of the first entry cut off after a truncation.
type segIndex struct {
	seg   uint64
	first uint64
	offs  []uint32
	end   uint32
}

// termRun is the term of every retained entry from index first to the next
// run's first.
type termRun struct {
	first uint64
	term  uint64
}

// entryLoc is where a synced entry's record lies: its segment and offset.
type entryLoc struct {
	seg uint64
	off int64
}

// memFirst is the index of the first entry still in memory, first+n when
// none is.
func (l *fileLog) memFirst() uint64 { return l.first + l.n - uint64(len(l.mem)) }

// tableOf is the position of the table that holds synced entry i, and of
// i in it; the caller checked that i is synced and retained.
func (l *fileLog) tableOf(i uint64) (tab, pos int) {
	tab = sort.Search(len(l.tables), func(k int) bool { return l.tables[k].first > i }) - 1
	return tab, int(i - l.tables[tab].first)
}

// spanEnd bounds from above the end of the record at position p of table t:
// the next entry's offset, or the table's end for its last one.
func (l *fileLog) spanEnd(t *segIndex, p int) int64 {
	if p+1 < len(t.offs) {
		return int64(t.offs[p+1]) * int64(recAlign)
	}
	return int64(t.end) * int64(recAlign)
}

// pushDisk appends synced entry index, whose record lies at [off, end) in
// segment seg, to the tables; hint is the capacity of a table it opens, 0
// to let it grow. A table left behind for another segment is clipped to its
// length, once: it takes no more entries. An offset a table cannot hold is
// refused, nothing changed.
func (l *fileLog) pushDisk(index, seg uint64, off, end int64, hint int) bool {
	if off < 0 || end <= off || end >= maxIndexedOffset {
		return false
	}
	o, e := uint32(off/int64(recAlign)), uint32((end+int64(recAlign)-1)/int64(recAlign))
	if k := len(l.tables) - 1; k >= 0 && l.tables[k].seg == seg && l.tables[k].first+uint64(len(l.tables[k].offs)) == index {
		l.tables[k].offs = append(l.tables[k].offs, o)
		l.tables[k].end = e
		return true
	}
	if k := len(l.tables) - 1; k >= 0 && cap(l.tables[k].offs) > len(l.tables[k].offs) {
		l.tables[k].offs = slices.Clone(l.tables[k].offs)
	}
	offs := make([]uint32, 1, max(hint, 1))
	offs[0] = o
	l.tables = append(l.tables, segIndex{seg: seg, first: index, offs: offs, end: e})
	return true
}

// pushTerm records that entry index, the next one, is of term term.
func (l *fileLog) pushTerm(index, term uint64) {
	if k := len(l.runs) - 1; k >= 0 && l.runs[k].term == term {
		return
	}
	l.runs = append(l.runs, termRun{first: index, term: term})
}

// termAt is the term of retained entry i; the caller checked that it is
// retained.
func (l *fileLog) termAt(i uint64) uint64 {
	lo, hi := 0, len(l.runs)
	for lo < hi {
		if m := int(uint(lo+hi) >> 1); l.runs[m].first <= i {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return l.runs[lo-1].term
}

// cutDiskFrom drops from the tables every entry from index i on.
func (l *fileLog) cutDiskFrom(i uint64) {
	k := len(l.tables)
	for k > 0 && l.tables[k-1].first >= i {
		k--
	}
	clear(l.tables[k:])
	l.tables = l.tables[:k]
	if k == 0 {
		return
	}
	t := &l.tables[k-1]
	if cut := i - t.first; cut < uint64(len(t.offs)) {
		t.end = t.offs[cut]
		t.offs = t.offs[:cut]
	}
}

// cutFrom drops every retained entry from index i on, in memory and on
// disk; i lies at or below first+n. Below first, the log restarts empty at
// i, the term before it unknown: a truncate record into a dropped segment
// removes entries gone with it, and a later compact record covers what
// follows.
func (l *fileLog) cutFrom(i uint64) {
	if i < l.first {
		l.clearIndex()
		l.first, l.prevTerm = i, 0
		return
	}
	if mf := l.memFirst(); i >= mf {
		clear(l.mem[i-mf:])
		l.mem = l.mem[:i-mf]
	} else {
		clear(l.mem)
		l.mem = l.mem[:0]
		l.cutDiskFrom(i)
	}
	k := len(l.runs)
	for k > 0 && l.runs[k-1].first >= i {
		k--
	}
	l.runs = l.runs[:k]
	l.n = i - l.first
}

// dropThrough drops every retained entry through index through, and the log
// starts past it, prev the term before it. Whole tables go, the one the point
// falls in is resliced: nothing proportional to the entries kept is copied
// (#1111). A point below the log changes nothing.
func (l *fileLog) dropThrough(through, prev uint64) {
	nf := through + 1
	if nf <= l.first {
		return
	}
	l.prevTerm = prev
	if nf >= l.first+l.n {
		l.clearIndex()
		l.first = nf
		return
	}
	if mf := l.memFirst(); nf > mf {
		d := nf - mf
		clear(l.mem[:d])
		l.mem = l.mem[d:]
	}
	k := 0
	for k < len(l.tables) && l.tables[k].first+uint64(len(l.tables[k].offs)) <= nf {
		k++
	}
	clear(l.tables[:k])
	l.tables = l.tables[k:]
	if len(l.tables) > 0 && l.tables[0].first < nf {
		t := &l.tables[0]
		t.offs = t.offs[nf-t.first:]
		t.first = nf
	}
	r := sort.Search(len(l.runs), func(k int) bool { return l.runs[k].first > nf }) - 1
	l.runs = l.runs[r:]
	l.runs[0].first = nf
	l.n -= nf - l.first
	l.first = nf
}

// dropCompacted drops the retained entries the compaction point covers,
// keeping the term of the last one dropped as the term before the log, the
// compaction's own past the log's end.
func (l *fileLog) dropCompacted() {
	prev := l.compactTerm
	if l.n > 0 {
		prev = l.termAt(min(l.compact, l.first+l.n-1))
	}
	l.dropThrough(l.compact, prev)
}

// lastTerm is the term of the last retained entry, the term before the log
// when it retains none.
func (l *fileLog) lastTerm() uint64 {
	if l.n == 0 {
		return l.prevTerm
	}
	return l.runs[len(l.runs)-1].term
}

// clearIndex empties the index; the caller sets where the log starts.
func (l *fileLog) clearIndex() {
	clear(l.tables)
	l.tables = l.tables[:0]
	l.runs = l.runs[:0]
	clear(l.mem)
	l.mem = l.mem[:0]
	l.n = 0
}

// relocate points the index at the records of the pending batch, now synced
// at l.off in segment seg, and releases the in-memory copies. Of the records
// of one index the batch holds, the last is the retained one: an entry
// truncated and written again in the same batch is written after its first
// record. An entry that does not come back — none does, in a batch Append
// and TruncateSuffix built — stays in memory. False when a table cannot hold
// an offset: the log stops on it.
func (l *fileLog) relocate(seg uint64) bool {
	mf, moved := l.memFirst(), 0
	var off int64
	for off < int64(len(l.pending)) {
		r, _ := parseRecord(l.pending[off:])
		var ok bool
		if moved, ok = l.relocateRecord(&r, seg, off, mf, moved); !ok {
			return false
		}
		off += int64(r.length)
	}
	clear(l.mem[:moved])
	n := copy(l.mem, l.mem[moved:])
	clear(l.mem[n:])
	l.mem = l.mem[:n]
	return true
}

// relocateRecord points the index at record r, which lies at off in the
// pending batch, when it is the in-memory entry the batch just synced, and
// returns how many in-memory entries have moved so far (moved, from mf on);
// false when a table cannot hold the offset.
func (l *fileLog) relocateRecord(r *record, seg uint64, off int64, mf uint64, moved int) (int, bool) {
	if r.kind != recEntry || r.index < mf || r.index >= mf+uint64(len(l.mem)) || l.mem[r.index-mf].Position.Term != r.term {
		return moved, true
	}
	k := int(r.index - mf)
	if k < moved {
		l.cutDiskFrom(r.index)
		moved = k
	}
	if k == moved {
		if !l.pushDisk(r.index, seg, l.off+off, l.off+off+int64(r.length), 0) {
			return moved, false
		}
		moved++
	}
	return moved, true
}

// retainsEntryIn reports whether a retained entry still lives in segment
// seg.
func (l *fileLog) retainsEntryIn(seg uint64) bool {
	for i := range l.tables {
		if l.tables[i].seg == seg && len(l.tables[i].offs) > 0 {
			return true
		}
	}
	return false
}

// memEntry is entry i of the unsynced tail; the caller checked i >= memFirst.
func (l *fileLog) memEntry(i uint64) coreconsensus.EntryValue { return l.mem[i-l.memFirst()] }
