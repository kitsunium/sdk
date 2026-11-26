package loop

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// fileCtl is one file body in flight on a slot (#982): what WriteFile and the
// backend's beginFile took, released by endFile. It lives in the ring's file
// table, not in connCtl, so a connection that sends no file pays none of it
// (#961); like connCtl it holds no pointer.
type fileCtl struct {
	// off is the next byte of the file to send, left how many are left.
	off  int64
	left int64
	// fd is the file being sent, the engine's own duplicate.
	fd int32
	// pipeR and pipeW are the pipe the file crosses on io_uring, a
	// connection's own, -1 on epoll; pipeHeld how many bytes the pipe holds.
	pipeR, pipeW int32
	pipeHeld     int32
	// err is the errno a file operation failed with, zero when none.
	err int32
	// ops counts the file's operations in flight on io_uring.
	ops uint8
	// bits holds ffEOF, ffFull and ffPoll.
	bits uint8
}

// fileSender is the optional side of a driver that sends a file's bytes from
// the kernel. Both native backends implement it; a platform without one has
// no driver at all, and LoopFileWriter refuses.
type fileSender interface {
	// beginFile takes what sending a file on slot needs — a duplicate of fd, a
	// pipe — and records it in a file record of the ring (takeFile); a refusal
	// leaves the slot untouched.
	beginFile(r *ring, slot int32, fd int) error
	// endFile releases what beginFile took, and the record (putFile), the file
	// done or the slot's last operation over: nothing of it is left in the
	// kernel.
	endFile(r *ring, slot int32)
}

// WriteFile implements corenet.LoopFileWriter: head, then the file's Len bytes
// from Off, queued behind the output. The descriptor is duplicated first, so
// the caller's own may close at once, and the duplicate stays the engine's until the file is
// done or the slot's last operation is over: a descriptor named in a queued
// SQE, or in the kernel's, cannot have been recycled under it. A file of the
// head alone is a Write.
func (lp *loop) WriteFile(ref corenet.LoopConnRef, head []byte, file corenet.LoopFileSpanValue) error {
	//: the loop's thread only, checked under loopdebug.
	if lp.threadOK != nil && !lp.threadOK() {
		//: refused.
		return corenet.LoopWrongThread
	}
	r, slot, ok := lp.lookup(ref)
	//: a closed connection takes no more output.
	if !ok {
		//: stale.
		return corenet.LoopStaleRef
	}
	//: nothing of the file: a plain write.
	if file.Len <= 0 {
		//: the head alone.
		return lp.Write(ref, head)
	}
	c := &r.conns[slot]
	//: one file at a time, and the head under the ceiling as a Write's bytes.
	if c.file != noFile || c.pending()+len(head) > lp.sz.outputCap {
		//: refused; OnWritable says when.
		return r.refuseOutput(c)
	}
	fs, ok := r.drv.(fileSender)
	//: a driver with no way to send a file.
	if !ok {
		//: refused, nothing queued.
		return corenet.LoopFileRefused
	}
	//: the descriptor and whatever the backend needs, or nothing.
	if err := fs.beginFile(r, slot, file.FD); err != nil {
		//: refused, nothing queued.
		return err
	}
	//: the head, in the output's order.
	if len(head) > 0 && !lp.queue(r, slot, c, head) {
		fs.endFile(r, slot)
		//: refused.
		return r.refuseOutput(c)
	}
	f := r.fileOf(slot)
	f.off, f.left = file.Off, file.Len
	r.markDirty(slot)
	//: queued; OnWritable says when its last byte left.
	return nil
}

// dropFile releases slot's file as the connection goes, no operation of it
// left in flight.
func (r *ring) dropFile(slot int32) {
	//: a file still attached: no operation of it remains, so it closes now.
	if fs, ok := r.drv.(fileSender); ok && r.conns[slot].file != noFile {
		fs.endFile(r, slot)
	}
}

// fileDone ends slot's file, its last byte gone: its descriptor and pipe
// released, a producer told, and the slot settled, a close having waited for it.
func (r *ring) fileDone(slot int32) {
	c := &r.conns[slot]
	//: both native backends send files.
	if fs, ok := r.drv.(fileSender); ok {
		fs.endFile(r, slot)
	}
	c.lastIO = r.lp.tick
	c.flags &^= fWantWritable
	r.markDirty(slot)
	//: told once the file has left; a closing connection hears nothing more.
	if c.state == stOpen {
		r.lp.h.OnWritable(r.lp, r.ref(slot))
	}
}

// takeFile gives slot a file record, released by putFile, and returns it.
// A pointer to it is good until the next takeFile, which may move the table.
func (r *ring) takeFile(slot, fd int32) *fileCtl {
	var idx int32
	//: a released record first, so the table grows only with the peak.
	if n := len(r.freeFiles); n > 0 {
		idx = r.freeFiles[n-1]
		r.freeFiles = r.freeFiles[:n-1]
	} else {
		idx = int32(len(r.files))
		r.files = append(r.files, fileCtl{})
	}
	r.files[idx] = fileCtl{fd: fd, pipeR: -1, pipeW: -1}
	r.conns[slot].file = idx
	//: the slot's.
	return &r.files[idx]
}

// fileOf returns slot's file record; the slot must have one.
func (r *ring) fileOf(slot int32) *fileCtl {
	//: by the slot's index.
	return &r.files[r.conns[slot].file]
}

// putFile releases slot's file record, its descriptors already closed.
func (r *ring) putFile(slot int32) {
	r.freeFiles = append(r.freeFiles, r.conns[slot].file)
	r.conns[slot].file = noFile
}
