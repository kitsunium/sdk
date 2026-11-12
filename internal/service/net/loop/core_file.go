package loop

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// fileSender is the optional side of a driver that sends a file's bytes from
// the kernel. Both native backends implement it; a platform without one has
// no driver at all, and LoopFileWriter refuses.
type fileSender interface {
	// beginFile takes what sending a file on slot needs — a duplicate of fd, a
	// pipe — and records it in the slot; a refusal leaves the slot untouched.
	beginFile(r *ring, slot int32, fd int) error
	// endFile releases what beginFile took, the file done or the slot's last
	// operation over: nothing of it is left in the kernel.
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
	if c.fileFD >= 0 || c.pending()+len(head) > lp.sz.outputCap {
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
	c.fileOff, c.fileLeft = file.Off, file.Len
	r.markDirty(slot)
	//: queued; OnWritable says when its last byte left.
	return nil
}

// dropFile releases slot's file as the connection goes, no operation of it
// left in flight.
func (r *ring) dropFile(slot int32) {
	//: a file still attached: no operation of it remains, so it closes now.
	if fs, ok := r.drv.(fileSender); ok && r.conns[slot].fileFD >= 0 {
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
