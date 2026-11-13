package seglog

import (
	"errors"
	"io"
	"os"
	"slices"
)

// rewritten is one entry a segment rewrite moved: its index and where it now
// lies.
type rewritten struct {
	index uint64
	at    slot
}

// Rewrite changes, durably and in place, records below index below: edit
// is called for every retained record below it, in index order, with its
// index, its kind byte and its payload — the caller's for the call only —
// and returns the kind and payload the record takes and true, or false to
// leave it as it is. A pending batch is synced first, as Sync syncs it.
//
// Every segment holding a record edit changed is written again whole —
// the same records at the same indexes, in the same batches with the same
// end flags, only the changed ones' kind and payload different — into a
// .tmp that is synced, renamed over the segment, and the directory synced:
// a crash leaves the old segment or the new one, never a mix, and recovery
// reads either. A segment edit changed nothing in is not touched. It
// returns how many segments it rewrote.
//
// A failure before a segment's rename leaves that segment as it was, and
// the log usable, and is returned (Failed, or Damaged for a segment that
// no longer parses); a failure from the rename on is final, as a failed
// Sync is.
func (l *Log) Rewrite(below uint64, edit func(index uint64, kind uint8, payload []byte) (uint8, []byte, bool)) (int, error) {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	if err := l.syncLocked(); err != nil {
		l.mu.Unlock()
		return 0, err
	}
	segs := slices.Clone(l.segs)
	first, next := l.first, l.next
	l.mu.Unlock()
	below = min(below, next)
	count := 0
	for i, s := range segs {
		if s.start >= below {
			break
		}
		end := next
		if i < len(segs)-1 {
			end = segs[i+1].start
		}
		if end <= first {
			// Only dropped records: the segment goes at the next drop.
			continue
		}
		changed, err := l.rewriteSegment(s, i == len(segs)-1, first, below, edit)
		if err != nil {
			return count, err
		}
		if changed {
			count++
		}
	}
	return count, nil
}

// rewriteSegment writes segment s again with the records edit changes, the
// last segment of the log when last. The caller holds wmu and nothing is
// pending, so no writer moves the segment or its records meanwhile.
func (l *Log) rewriteSegment(
	s *segment, last bool, first, below uint64,
	edit editFunc,
) (bool, error) {
	limit := s.size
	if last {
		limit = l.off
	}
	data := make([]byte, limit)
	if _, err := s.f.ReadAt(data, 0); err != nil && !errors.Is(err, io.EOF) {
		return false, failed("read segment", err)
	}
	res, err := rebuildSegment(&rebuildPlan{n: s.n, data: data, last: last, first: first, below: below, edit: edit})
	if err != nil || !res.changed {
		return false, err
	}
	out, moved := res.data, res.moved
	size := int64(len(out))
	if last {
		// The segment being written keeps its size, and grows when an edit
		// lengthened its records: the seal a later rotation writes must still
		// fit after them, or that rotation seals nothing and recovery refuses
		// the segment as a sealed one with a hole.
		size = max(s.size, size+sealReserve)
	}
	if err := l.writeTmp(s.n, out, size); err != nil {
		return false, err
	}
	return true, l.publishRewrite(s, out, size, last, moved)
}

// editFunc is the edit a rewrite offers every retained record: it returns
// the kind and payload the record takes and true, or false to keep it.
type editFunc func(index uint64, kind uint8, payload []byte) (uint8, []byte, bool)

// rebuildPlan is what rebuildSegment is asked to do: parse data, the bytes
// of segment n, and offer every entry from first up to below excluded to
// edit. last says data is the end of the log, where a record that does not
// parse is the torn tail and not damage.
type rebuildPlan struct {
	data         []byte
	edit         editFunc
	n            uint64
	first, below uint64
	last         bool
}

// rebuilt is the outcome of rebuildSegment: the new bytes of the segment,
// where each retained entry now lies, and whether edit changed any.
type rebuilt struct {
	data    []byte
	moved   []rewritten
	changed bool
}

// rebuildSegment parses the segment bytes p.data and encodes them again,
// every entry from p.first up to below excluded offered to p.edit. A sealed
// segment is read through its seal; the last one up to its end.
func rebuildSegment(p *rebuildPlan) (rebuilt, error) {
	res := rebuilt{data: make([]byte, 0, len(p.data))}
	for off := 0; off < len(p.data); {
		r, ok := parseRecord(p.data[off:])
		if !ok {
			if p.last {
				break
			}
			return rebuilt{}, damaged(p.n, int64(off), "record does not parse in a rewrite")
		}
		off += r.length
		if res.encode(p, &r) {
			break
		}
	}
	return res, nil
}

// encode appends record r to the new bytes, after offering it to the edit
// when it is an entry in the range, and notes where an entry now lies. It
// reports whether r was the seal, which ends the segment.
func (b *rebuilt) encode(p *rebuildPlan, r *record) bool {
	if r.kind == recEntry && r.index >= p.first && r.index < p.below {
		if kind, payload, edited := p.edit(r.index, r.tag, r.data); edited {
			r.tag, r.data, b.changed = kind, payload, true
		}
	}
	if r.kind == recSeal {
		r.index = uint64(len(b.data))
	}
	at := len(b.data)
	b.data = appendRecord(b.data, r)
	if r.kind == recEntry && r.index >= p.first {
		b.moved = append(b.moved, rewritten{index: r.index, at: slot{seg: p.n, off: int64(at), size: uint32(len(r.data)), kind: r.tag}})
	}
	return r.kind == recSeal
}

// writeTmp writes out, zero-filled to size, into segment n's .tmp and syncs
// it. A failure removes the .tmp and leaves the log as it was.
func (l *Log) writeTmp(n uint64, out []byte, size int64) error {
	tmp := l.tmpPath(n)
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return failed("create segment", err)
	}
	// Whatever fails below leaves an unpublished .tmp: the handle is closed
	// first (Windows refuses to remove an open file) and the file removed.
	// After the explicit Close the second one only reports ErrClosed, which
	// nothing reads.
	defer func() {
		if err != nil {
			abandon(f.Close())
			abandon(os.Remove(tmp))
		}
	}()
	if err = fillTmp(f, out, size); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		err = failed("close segment", err)
		return err
	}
	return nil
}

// writeSyncer is the part of a segment file fillTmp writes through.
type writeSyncer interface {
	io.WriterAt
	Sync() error
}

// fillTmp writes out at the start of f, zero-fills it up to size and syncs
// it.
func fillTmp(f writeSyncer, out []byte, size int64) error {
	if _, err := f.WriteAt(out, 0); err != nil {
		return failed("write segment", err)
	}
	if err := zeroRange(f, int64(len(out)), size); err != nil {
		return failed("zero-fill", err)
	}
	if err := f.Sync(); err != nil {
		return failed("sync segment", err)
	}
	return nil
}

// publishRewrite renames segment s's .tmp over it, reopens it, moves the
// entries it holds to where the rewrite put them and syncs the directory.
// The old file is closed before the rename: Windows refuses to replace a
// file a handle holds open. A rename refused leaves the old segment, which
// is reopened; anything after it is final.
func (l *Log) publishRewrite(s *segment, out []byte, size int64, last bool, moved []rewritten) error {
	final, tmp := l.segPath(s.n), l.tmpPath(s.n)
	l.mu.Lock()
	if err := s.f.Close(); err != nil {
		l.failed = failed("close segment", err)
		l.mu.Unlock()
		abandon(os.Remove(tmp))
		return l.failed
	}
	renameErr := os.Rename(tmp, final)
	f, err := os.OpenFile(final, os.O_RDWR, 0)
	if err != nil {
		l.failed = failed("open segment", err)
		l.mu.Unlock()
		return l.failed
	}
	s.f = f
	if renameErr != nil {
		l.mu.Unlock()
		abandon(os.Remove(tmp))
		return failed("rename segment", renameErr)
	}
	s.size = size
	for _, m := range moved {
		if m.index >= l.first && m.index < l.next {
			l.slots[m.index-l.first] = m.at
		}
	}
	if last {
		l.off = int64(len(out))
	}
	l.mu.Unlock()
	if err := syncDir(l.dir); err != nil {
		l.mu.Lock()
		l.failed = failed("sync dir", err)
		l.mu.Unlock()
		return l.failed
	}
	return nil
}
