package http1

import (
	"errors"
	"io"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// copyChunk is the largest chunk one worker job reads for the copy path:
	// the size a step of the zero-copy path sends too.
	copyChunk int64 = 64 << 10
	// copyMinChunk is the smallest chunk: an output ceiling under it would make
	// the copy path write nothing at all.
	copyMinChunk int64 = 4 << 10
	// copyStall is how long a chunk the output refused may wait for OnWritable: a
	// client slower than a chunk's worth of drain in this long stops holding the
	// file. Every backend reports a failed write as the connection's end (the
	// portable writer posts it itself since #982), so the timer bounds a slow
	// peer, not a lost event.
	copyStall time.Duration = 10 * time.Second
)

// The bits of fileBody.flags.
const (
	// bodyActive says a file's body is leaving.
	bodyActive uint8 = 1 << iota
	// bodyNative says the engine owns the file: OnWritable ends it, and no
	// reader is held here.
	bodyNative
	// bodyClosing says the connection closes after the file.
	bodyClosing
	// bodyBusy says a chunk is out with the worker, the reader the worker's
	// until it comes back.
	bodyBusy
)

// fileBody is a file answer whose body is still leaving: the connection's held
// answers wait behind it. On the native path it is only its flags; on the
// copy path it is the file the worker reads and the chunk the output refused.
type fileBody struct {
	// flags holds the bodyActive, bodyNative, bodyClosing and bodyBusy bits.
	flags uint8
	// r reads the file; off is the next byte and left the bytes still to send.
	r    io.ReaderAt
	off  int64
	left int64
	// slot holds a chunk the output refused, waiting for OnWritable; nil when
	// none.
	slot *pendSlot
}

// active reports whether a file's body is still leaving; a slot that never
// sent a file has none.
func (b *fileBody) active() bool {
	//: set while it leaves.
	return b != nil && b.flags&bodyActive != 0
}

// setFile starts the connection's file body, in the slot's kept one when it
// has one.
func (f *connFlag) setFile(b fileBody) {
	//: the slot's first file.
	if f.file == nil {
		f.file = &b
		//: allocated once.
		return
	}
	*f.file = b
}

// clearFile ends the connection's file body, the allocation kept.
func (f *connFlag) clearFile() {
	//: a body to empty.
	if f.file != nil {
		*f.file = fileBody{}
	}
}

// fileStart is one file answer about to leave: what startFile, startNative and
// startCopy are passed.
type fileStart struct {
	// io and ref are the connection.
	io  corenet.LoopIO
	ref corenet.LoopConnRef
	// st is the lane's state, f the connection's flag.
	st *loopState
	f  *connFlag
	// out holds the bytes before the file, the head included.
	out []byte
	// file is the body.
	file corenet.HTTP1FileValue
	// closing says the connection closes after the file.
	closing bool
}

// jobCanceller is the internal capability of a link that lends a job an entry
// of its own: told when the job will never run, so the entry goes back.
type jobCanceller interface {
	// CancelJob gives back the job's entry; the job's bits say whose it is.
	CancelJob(lane int, job uint64)
}

// findCanceller walks the chain down to the first link that holds job entries.
//
// IFACE-OPAQUE: an internal capability of a chain link, found by the walk,
// returned as the type it is tested for.
func findCanceller(h corenet.HTTP1Handler) jobCanceller {
	//: down the chain, every link unwrapped.
	for {
		//: the first link that lends entries.
		if c, ok := h.(jobCanceller); ok {
			//: found.
			return c
		}
		l, ok := h.(interface{ wrapped() corenet.HTTP1Handler })
		//: a handler that wraps nothing: none.
		if !ok {
			//: none.
			return nil
		}
		h = l.wrapped()
	}
}

// cancelJob gives back the entry of a job that will not run, when a link holds
// one.
func (a *LoopHandler) cancelJob(lane int, job uint64) {
	//: a chain with no such link has nothing to give back.
	if a.canceller != nil {
		a.canceller.CancelJob(lane, job)
	}
}

// closeFileReader closes r when it can be closed, on every path a file's
// answer ends; closing a nil reader changes nothing.
func closeFileReader(r io.ReaderAt) {
	//: only a closer has anything to give back.
	if c, ok := r.(io.Closer); ok {
		swallowErr(c.Close())
	}
}

// startFile sends s: by the backend's splice or sendfile when it has
// LoopFileWriter and the reader a descriptor, else through the copy path.
func (a *LoopHandler) startFile(s *fileStart) {
	//: the kernel takes the file: no byte of it through the process.
	if fw, ok := s.io.(corenet.LoopFileWriter); ok {
		//: a descriptor to hand it.
		if fd, ok := s.file.R.(interface{ Fd() uintptr }); ok && a.startNative(s, fw, int(fd.Fd())) {
			//: queued, or the connection given up on.
			return
		}
	}
	a.startCopy(s)
}

// startNative queues the head and the file on the engine and reports true, or
// false for a refusal the copy path can still answer. The engine holds its own
// duplicate once WriteFile returned, so the reader closes at once.
func (a *LoopHandler) startNative(s *fileStart, fw corenet.LoopFileWriter, fd int) bool {
	err := fw.WriteFile(s.ref, s.out, corenet.LoopFileSpanValue{FD: fd, Off: s.file.Off, Len: s.file.Len})
	//: refused for a reason the copy path has no descriptor for.
	if errors.Is(err, corenet.LoopFileRefused) {
		//: the copy path answers.
		return false
	}
	closeFileReader(s.file.R)
	//: stale, or the head past the ceiling: this connection is over.
	if err != nil {
		s.f.dead = true
		swallowErr(s.io.Close(s.ref))
		s.st.dropHeld(s.f)
		//: given up on.
		return true
	}
	s.f.setFile(fileBody{flags: bodyActive | bodyNative})
	//: the engine holds the close until the file's last byte.
	if s.closing {
		swallowErr(s.io.Close(s.ref))
	}
	//: queued.
	return true
}

// startCopy writes the head and starts the copy path: the file read in chunks
// on the worker, each written through the output's ceiling.
func (a *LoopHandler) startCopy(s *fileStart) {
	//: the output refuses the head, or the connection is gone.
	if err := s.io.Write(s.ref, s.out); err != nil {
		closeFileReader(s.file.R)
		s.f.dead = true
		swallowErr(s.io.Close(s.ref))
		s.st.dropHeld(s.f)
		//: given up on.
		return
	}
	flags := bodyActive
	//: the close the answer asked, after the file's last byte.
	if s.closing {
		flags |= bodyClosing
	}
	s.f.setFile(fileBody{flags: flags, r: s.file.R, off: s.file.Off, left: s.file.Len})
	a.copyNext(s.io, s.ref, s.st, s.f)
}

// copyNext sends the next chunk's job to the lane's worker; a queue that is
// full retries at the batch's end.
func (a *LoopHandler) copyNext(io corenet.LoopIO, ref corenet.LoopConnRef, st *loopState, f *connFlag) {
	//: a misuse: no worker to read with.
	if st.outbound == nil {
		a.endCopy(io, ref, st, f, true)
		//: given up on.
		return
	}
	idx, slot := st.takeSlot()
	slot.ref = ref
	slot.copy = slotCopy(slot, a.chunk())
	slot.copy.r, slot.copy.off, slot.copy.n, slot.copy.err = f.file.r, f.file.off, 0, nil
	slot.copy.buf = slot.copy.buf[:min(int64(cap(slot.copy.buf)), f.file.left)]
	//: queued: the file is the worker's until it comes back.
	if st.outbound.Send(io, corenet.LoopJobValue{Ref: ref, Slot: idx, Gen: slot.gen}) == nil {
		f.file.flags |= bodyBusy
		//: sent.
		return
	}
	slot.copy = nil
	st.freeSlot(idx)
	st.copyRetry = append(st.copyRetry, ref)
}

// slotCopy returns slot's chunk job, its buffer of the chunk size kept from an
// earlier one.
func slotCopy(slot *pendSlot, chunk int64) *copyJob {
	//: reused, or the first of this slot.
	if slot.buf == nil {
		slot.buf = make([]byte, 0, chunk)
	}
	slot.job.buf = slot.buf[:0]
	//: the slot's own job value.
	return &slot.job
}

// chunk is the copy path's chunk size: at most half of the output ceiling, so
// a chunk fits with its neighbour, bounded both ways.
func (a *LoopHandler) chunk() int64 {
	//: inside the bounds.
	return max(copyMinChunk, min(copyChunk, int64(a.holdCap)/2))
}

// workCopy reads one chunk, on the lane's worker. A connection gone meanwhile
// costs nothing but this read: OnDone gives the slot back.
func workCopy(slot *pendSlot) {
	job := slot.copy
	//: an empty chunk has nothing to read.
	if len(job.buf) == 0 {
		//: nothing.
		return
	}
	job.n, job.err = job.r.ReadAt(job.buf, job.off)
}

// copyDone hands the chunk the worker read to the output, or ends the file: a
// connection gone closes the file, a short read closes the connection, the
// declared length no longer honoured.
func (a *LoopHandler) copyDone(io corenet.LoopIO, st *loopState, slot *pendSlot) {
	job, ref := slot.copy, slot.ref
	f := st.peek(ref)
	//: the connection left while the worker read: the file with it.
	if slot.cancelled.Load() || f == nil || !f.file.active() {
		closeFileReader(job.r)
		slot.copy = nil
		st.freeSlot(slot.idx)
		//: dropped.
		return
	}
	f.file.flags &^= bodyBusy
	//: the file shrank under the worker, or failed: the client sees a short body.
	if job.n == 0 && f.file.left > 0 {
		slot.copy = nil
		st.freeSlot(slot.idx)
		a.endCopy(io, ref, st, f, true)
		//: over.
		return
	}
	f.file.slot = slot
	a.writeChunk(io, ref, st, f)
}

// writeChunk writes the chunk the slot holds; a refused one stays there until
// OnWritable, the output's bound doing the back-pressure.
func (a *LoopHandler) writeChunk(io corenet.LoopIO, ref corenet.LoopConnRef, st *loopState, f *connFlag) {
	slot := f.file.slot
	job := slot.copy
	err := io.Write(ref, job.buf[:job.n])
	//: no room: OnWritable retries it; the slot stays held, under a stall timer.
	if errors.Is(err, corenet.LoopOutputFull) {
		swallowErr(io.SetDeadline(ref, copyStall))
		//: waiting.
		return
	}
	swallowErr(io.SetDeadline(ref, 0))
	f.file.slot = nil
	n := int64(job.n)
	slot.copy = nil
	st.freeSlot(slot.idx)
	//: the connection is over.
	if err != nil {
		a.endCopy(io, ref, st, f, true)
		//: over.
		return
	}
	f.file.off += n
	f.file.left -= n
	//: its last byte.
	if f.file.left == 0 {
		a.endCopy(io, ref, st, f, false)
		//: done.
		return
	}
	a.copyNext(io, ref, st, f)
}

// endCopy closes the copy path's reader and ends the file: failed, the
// connection closes with what it has, the short body showing; else the
// answers behind it go on, or the close the answer asked.
func (a *LoopHandler) endCopy(io corenet.LoopIO, ref corenet.LoopConnRef, st *loopState, f *connFlag, failed bool) {
	closeFileReader(f.file.r)
	closing := f.file.flags&bodyClosing != 0
	f.clearFile()
	//: over, one way or the other.
	if failed || closing {
		f.dead = true
		swallowErr(io.Close(ref))
		st.dropHeld(f)
		//: closed once its output has left.
		return
	}
	//: the held answers behind it, at the batch's end.
	st.markReady(ref, f)
}

// fileWritable is OnWritable for a connection with a file leaving: a native
// file is done; a copy resumes the chunk the output refused.
func (a *LoopHandler) fileWritable(io corenet.LoopIO, ref corenet.LoopConnRef, st *loopState, f *connFlag) {
	//: the engine's last byte: the held answers go on.
	if f.file.flags&bodyNative != 0 {
		f.clearFile()
		a.flushHeld(io, ref, st, f)
		//: resumed.
		return
	}
	//: a refused chunk is the only thing waiting for room.
	if f.file.slot != nil {
		a.writeChunk(io, ref, st, f)
	}
}

// retryCopies sends again the chunks a full worker queue refused.
func (a *LoopHandler) retryCopies(io corenet.LoopIO, st *loopState) {
	retry := st.copyRetry
	st.copyRetry = st.copyRetry[len(retry):]
	//: each connection once more.
	for _, ref := range retry {
		f := st.peek(ref)
		//: still copying, and not waiting for anything else.
		if f != nil && f.file.active() && f.file.flags&(bodyNative|bodyBusy) == 0 && f.file.slot == nil {
			a.copyNext(io, ref, st, f)
		}
	}
	//: nothing kept across batches but what copyNext appended again.
	if len(st.copyRetry) == 0 {
		st.copyRetry = retry[:0]
	}
}

// closedFile gives up a file still leaving when its connection closed: a chunk
// out with the worker is dropped at its completion, anything else closes now; a
// native file is the engine's.
func closedFile(f *connFlag, st *loopState) {
	//: nothing leaving, or the engine's own.
	if !f.file.active() || f.file.flags&bodyNative != 0 {
		f.clearFile()
		//: done.
		return
	}
	//: out with the worker: its completion closes it.
	if f.file.flags&bodyBusy == 0 {
		closeFileReader(f.file.r)
		//: a refused chunk's slot goes back too.
		if s := f.file.slot; s != nil {
			s.copy = nil
			st.freeSlot(s.idx)
		}
	}
	f.clearFile()
}
