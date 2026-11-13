package seglog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// segment is one published segment file, open for reading and writing.
type segment struct {
	n uint64
	f *os.File
	// size is the file's fixed size.
	size int64
	// start is the index its first entry takes: its checkpoint's next.
	start uint64
}

// segName is segment n's file name; fixed-width hex, so the names sort in
// segment order and recovery accepts no other spelling of n.
func segName(n uint64) string { return fmt.Sprintf("seg-%016x", n) }

// segPath is the path of segment n's file.
func (l *Log) segPath(n uint64) string { return filepath.Join(l.dir, segName(n)) }

// tmpPath is the path of segment n's file before its publication.
func (l *Log) tmpPath(n uint64) string { return l.segPath(n) + ".tmp" }

// zeroFrom zeroes f from off to size, 1 MiB at a time, and syncs it; the
// sync runs even when there is nothing to zero.
func zeroFrom(f *os.File, off, size int64) error {
	if err := zeroRange(f, off, size); err != nil {
		return err
	}
	return datasync(f)
}

// zeroRange writes zeros over f from off to size, 1 MiB at a time, and
// syncs nothing.
func zeroRange(f io.WriterAt, off, size int64) error {
	if off >= size {
		return nil
	}
	zero := make([]byte, min(size-off, 1<<20))
	for p := off; p < size; {
		n := min(int64(len(zero)), size-p)
		if _, err := f.WriteAt(zero[:n], p); err != nil {
			return err
		}
		p += n
	}
	return nil
}

// abandonTmp closes f, the segment .tmp at path a failure leaves
// unpublished, removes it, and returns c, the failure being reported. A
// removal that fails leaves it to recovery, which deletes every .tmp.
func abandonTmp(f io.Closer, path string, c error) error {
	abandon(f.Close())
	abandon(os.Remove(path))
	return c
}

// createSegment publishes segment n opening with cp: a zero-filled .tmp,
// the checkpoint, a sync, the rename, a sync of the directory. size is the
// segment's size.
func (l *Log) createSegment(n uint64, size int64, cp *checkpoint) error {
	cpRec := appendRecord(nil, &record{kind: recCheckpt, end: true, batch: cp.nextBatch, data: cp.encode()})
	tmp := l.tmpPath(n)
	f, err := l.segmentFile(n, size, tmp)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(cpRec, 0); err != nil {
		return abandonTmp(f, tmp, failed("checkpoint", err))
	}
	if err := f.Sync(); err != nil {
		return abandonTmp(f, tmp, failed("sync segment", err))
	}
	// Closed before the rename and opened again after: Windows refuses to
	// rename a file a handle holds open.
	if err := f.Close(); err != nil {
		return failed("close segment", err)
	}
	final := l.segPath(n)
	if err := os.Rename(tmp, final); err != nil {
		return failed("rename segment", err)
	}
	if err := syncDir(l.dir); err != nil {
		return failed("sync dir", err)
	}
	f, err = os.OpenFile(final, os.O_RDWR, 0)
	if err != nil {
		return failed("open segment", err)
	}
	l.segs = append(l.segs, &segment{n: n, f: f, size: size, start: cp.next})
	l.off = int64(len(cpRec))
	l.batch = cp.nextBatch + 1
	l.prepare(n+1, l.segBytes)
	return nil
}

// segmentFile is the zero-filled .tmp of segment n at size: the prepared
// one when it is that segment at that size, else one created and zeroed
// now, on the writer's path.
func (l *Log) segmentFile(n uint64, size int64, tmp string) (*os.File, error) {
	if f := l.takePrepared(n, size); f != nil {
		return f, nil
	}
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, failed("create segment", err)
	}
	if err := zeroFrom(f, 0, size); err != nil {
		return nil, abandonTmp(f, tmp, failed("zero-fill", err))
	}
	return f, nil
}

// prepare launches the goroutine that zero-fills segment n off the
// writer's path. It ends on its own once the .tmp is zeroed or has failed,
// closing done; takePrepared and Close only ever wait on it.
func (l *Log) prepare(n uint64, size int64) {
	done := make(chan struct{})
	p := &prepared{n: n, size: size, done: done}
	l.prep = p
	tmp := l.tmpPath(n)
	go func() {
		defer close(done)
		f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			p.err = err
			return
		}
		if err := zeroFrom(f, 0, size); err != nil {
			// A half-zeroed .tmp is never published: the writer builds the
			// segment itself, where a failure stops the log.
			p.err = abandonTmp(f, tmp, err)
			return
		}
		p.f = f
	}()
}

// takePrepared waits for the prepared segment n, when it is the one wanted
// at that size; nil means prepare it now.
func (l *Log) takePrepared(n uint64, size int64) *os.File {
	p := l.prep
	l.prep = nil
	if p == nil {
		return nil
	}
	<-p.done
	if p.err != nil || p.n != n || p.size != size {
		if p.f != nil {
			// The wrong segment or the wrong size: removed now rather than
			// at recovery.
			abandon(abandonTmp(p.f, l.tmpPath(p.n), nil))
		}
		return nil
	}
	return p.f
}

// rotate seals the current segment in its own synced batch, then publishes
// the next one. It runs between batches only. need is the size of the
// batch that did not fit.
func (l *Log) rotate(need int) error {
	cur := l.segs[len(l.segs)-1]
	seal := appendRecord(nil, &record{kind: recSeal, end: true, batch: l.batch, index: uint64(l.off)})
	if l.off+int64(len(seal)) <= cur.size {
		if _, err := cur.f.WriteAt(seal, l.off); err != nil {
			return failed("seal", err)
		}
		if err := datasync(cur.f); err != nil {
			return failed("sync seal", err)
		}
		l.batch++
		if l.afterSeal != nil {
			l.afterSeal()
		}
	}
	return l.publishNext(need)
}

// publishNext publishes the segment after the current one, whose seal is
// durable, from the durable state and the next batch number. A batch
// larger than a segment gets a segment of its own size.
func (l *Log) publishNext(need int) error {
	cur := l.segs[len(l.segs)-1]
	cp := checkpoint{next: l.dur.next, first: l.dur.first, nextBatch: l.batch}
	size := l.segBytes
	if int64(need)+growSlack+sealReserve > size {
		size = int64(align8(need)) + growSlack + sealReserve
	}
	return l.createSegment(cur.n+1, size, &cp)
}

// dropSegmentsLocked unlinks the prefix of sealed segments that hold only
// records below the first index, then syncs the directory. It runs after a
// sync, so the drop record that justifies it is durable first.
func (l *Log) dropSegmentsLocked() error {
	removed := false
	for len(l.segs) > 1 && l.segs[1].start <= l.first {
		if err := l.segs[0].f.Close(); err != nil {
			return failed("close segment", err)
		}
		if err := os.Remove(l.segPath(l.segs[0].n)); err != nil {
			return failed("remove segment", err)
		}
		l.segs = l.segs[1:]
		removed = true
	}
	if removed {
		if err := syncDir(l.dir); err != nil {
			return failed("sync dir", err)
		}
	}
	return nil
}
