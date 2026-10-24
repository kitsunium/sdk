//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	// uringEntries is the SQ size asked for; CLAMP lets the kernel lower it,
	// and the size it grants is read back from the params.
	uringEntries uint32 = 4096
	// cqFactor is how many times uringEntries CQSIZE asks for the CQ.
	cqFactor uint32 = 4
	// bufGroup is the provided-buffer group every recv selects from.
	bufGroup uint16 = 1
	// defaultBufs is how many provided buffers a ring has.
	defaultBufs uint32 = 4096
	// defaultBufSize is the size of one provided buffer, in bytes.
	defaultBufSize uint32 = 4096
	// enterRetries bounds the retries of a busy submit.
	enterRetries int = 8
)

// uring is one io_uring instance: its mappings, the views into them, and its
// provided-buffer ring. It belongs to one loop thread.
type uring struct {
	fd     int
	params uringParams
	// ringMem is the SQ and CQ rings' single mapping; sqeMem the SQEs'.
	ringMem []byte
	sqeMem  []byte
	// The SQ ring's fields, in the mapping.
	sqHead  *uint32
	sqTail  *uint32
	sqFlags *uint32
	sqMask  uint32
	sqLen   uint32
	sqes    []sqe
	// tail is the next SQE this thread fills; submitted the tail last handed
	// to the kernel.
	tail      uint32
	submitted uint32
	// The CQ ring's fields, in the mapping.
	cqHead *uint32
	cqTail *uint32
	cqMask uint32
	cqLen  uint32
	cqes   []cqe
	// bufRing is the provided-buffer ring's mapping; bufs the buffers'.
	bufRing   []byte
	bufMem    []byte
	bufCount  uint32
	bufSize   uint32
	bufTail   uint16
	shadowBid uint16
	// retries counts busy submits retried.
	retries uint64
}

// openURing sets an io_uring up with flags, mapping it and building the
// views. It returns the errno of the step that failed.
func openURing(flags uint32) (*uring, string, syscall.Errno) {
	u := &uring{fd: -1}
	u.params.flags = flags | setupCQSize | setupClamp
	u.params.cqEntries = uringEntries * cqFactor
	fd, errno := uringSetup(uringEntries, &u.params)
	//: the kernel refused the setup.
	if errno != 0 {
		//: the caller decides whether to retry with fewer flags.
		return nil, "setup", errno
	}
	u.fd = fd
	p := &u.params
	//: the probe requires all three; without them the loop cannot wait with a
	//: timeout, could lose completions, or would need two mappings.
	if p.features&featExtArg == 0 || p.features&featNoDrop == 0 || p.features&featSingleMmap == 0 {
		u.close()
		//: refused by feature.
		return nil, "features", syscall.EOPNOTSUPP
	}
	//: the rings and the SQEs, mapped; a failure names its mapping.
	if step, errno := u.mapRings(); errno != 0 {
		u.close()
		//: refused.
		return nil, step, errno
	}
	u.buildViews()
	//: ready.
	return u, "", 0
}

// mapRings maps the SQ and CQ rings, one mapping under FEAT_SINGLE_MMAP, and
// the SQEs. It returns the step that failed and the kernel's errno.
func (u *uring) mapRings() (step string, errno syscall.Errno) {
	p := &u.params
	sqSize := uintptr(p.sqOff.array) + uintptr(p.sqEntries)*4
	cqSize := uintptr(p.cqOff.cqes) + uintptr(p.cqEntries)*unsafe.Sizeof(cqe{})
	size := max(sqSize, cqSize)
	mem, err := syscall.Mmap(u.fd, int64(offSQRing), int(size), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED|syscall.MAP_POPULATE)
	//: the rings could not be mapped.
	if err != nil {
		//: refused.
		return "mmap rings", errnoOfErr(err)
	}
	u.ringMem = mem
	sqeBytes, err := syscall.Mmap(u.fd, int64(offSQEs), int(uintptr(p.sqEntries)*unsafe.Sizeof(sqe{})), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED|syscall.MAP_POPULATE)
	//: the SQEs could not be mapped.
	if err != nil {
		//: refused.
		return "mmap sqes", errnoOfErr(err)
	}
	u.sqeMem = sqeBytes
	//: mapped.
	return "", 0
}

// buildViews points the ring's fields into its mappings, and maps SQ slot i
// to SQE i once and for all.
func (u *uring) buildViews() {
	p := &u.params
	base := unsafe.Pointer(&u.ringMem[0])
	u.sqHead = (*uint32)(unsafe.Add(base, p.sqOff.head))
	u.sqTail = (*uint32)(unsafe.Add(base, p.sqOff.tail))
	u.sqFlags = (*uint32)(unsafe.Add(base, p.sqOff.flags))
	u.sqMask = *(*uint32)(unsafe.Add(base, p.sqOff.ringMask))
	u.sqLen = p.sqEntries
	u.cqHead = (*uint32)(unsafe.Add(base, p.cqOff.head))
	u.cqTail = (*uint32)(unsafe.Add(base, p.cqOff.tail))
	u.cqMask = *(*uint32)(unsafe.Add(base, p.cqOff.ringMask))
	u.cqLen = p.cqEntries
	u.cqes = unsafe.Slice((*cqe)(unsafe.Add(base, p.cqOff.cqes)), p.cqEntries)
	u.sqes = unsafe.Slice((*sqe)(unsafe.Pointer(&u.sqeMem[0])), p.sqEntries)
	array := unsafe.Slice((*uint32)(unsafe.Add(base, p.sqOff.array)), p.sqEntries)
	//: the SQ array maps slot i to SQE i once and for all.
	for i := range array {
		array[i] = uint32(i)
	}
	u.tail = atomic.LoadUint32(u.sqTail)
	u.submitted = u.tail
}

// errnoOfErr extracts an errno, EIO otherwise.
func errnoOfErr(err error) syscall.Errno {
	//: the kernel's own.
	if e, ok := err.(syscall.Errno); ok {
		//: as is.
		return e
	}
	//: anything else.
	return syscall.EIO
}

// close releases every mapping and the ring.
func (u *uring) close() {
	//: each mapping that exists.
	for _, m := range [][]byte{u.bufRing, u.bufMem, u.sqeMem, u.ringMem} {
		//: mapped.
		if m != nil {
			swallowErr(syscall.Munmap(m))
		}
	}
	u.bufRing, u.bufMem, u.sqeMem, u.ringMem = nil, nil, nil, nil
	//: the ring itself.
	if u.fd >= 0 {
		swallowErr(syscall.Close(u.fd))
		u.fd = -1
	}
}

// get returns a zeroed SQE, submitting what is queued when the SQ is full.
// It returns nil only when the kernel takes nothing after the bounded
// retries.
func (u *uring) get() *sqe {
	//: room, or room made by submitting.
	for i := 0; u.tail-atomic.LoadUint32(u.sqHead) >= u.sqLen; i++ {
		//: the kernel never drained the SQ.
		if i >= enterRetries {
			//: the caller counts the failure.
			return nil
		}
		u.submit()
	}
	s := &u.sqes[u.tail&u.sqMask]
	*s = sqe{}
	u.tail++
	//: the caller fills it; publish makes it the kernel's.
	return s
}

// publish hands every filled SQE to the kernel's view of the SQ tail.
func (u *uring) publish() {
	//: the SQEs are written before the tail moves.
	atomic.StoreUint32(u.sqTail, u.tail)
}

// pending is how many SQEs are published but not yet submitted.
func (u *uring) pending() uint32 {
	//: the difference of two counters, wrapping.
	return u.tail - u.submitted
}

// submit submits every pending SQE without waiting. EINTR retries; EBUSY and
// EAGAIN return so the caller reaps and retries, counted.
func (u *uring) submit() {
	u.publish()
	//: bounded, so a kernel that keeps refusing cannot spin the loop.
	for range enterRetries {
		n := u.pending()
		//: nothing to submit.
		if n == 0 {
			//: done.
			return
		}
		got, errno := uringEnterSubmit(u.fd, n)
		//: by outcome.
		switch errno {
		//: taken, some or all.
		case 0:
			u.submitted += uint32(got)
		//: interrupted: again.
		case syscall.EINTR:
		//: the CQ is full or the kernel is short: the caller reaps first.
		default:
			u.retries++
			//: reaped by the caller, retried at the next batch.
			return
		}
	}
}

// wait submits what is pending and blocks until one completion or the
// timeout, in one io_uring_enter with EXT_ARG. ETIME is the tick.
func (u *uring) wait(ts *kernelTimespec) {
	u.publish()
	arg := getEventsArg{ts: uint64(uintptr(unsafe.Pointer(ts)))}
	//: a completion already there makes the wait a submit.
	minComplete := uint32(1)
	if u.ready() > 0 {
		minComplete = 0
	}
	got, errno := uringEnterWait(u.fd, u.pending(), minComplete, enterGetEvents|enterExtArg, unsafe.Pointer(&arg), unsafe.Sizeof(arg))
	//: by outcome; the CQ is peeked by the caller whatever happened.
	switch errno {
	//: submitted, and woken.
	case 0:
		u.submitted += uint32(got)
	//: the tick, an interruption, or a busy kernel: the caller reaps.
	case errnoETIME, syscall.EINTR:
	default:
		u.retries++
	}
}

// ready is how many completions wait in the CQ.
func (u *uring) ready() uint32 {
	//: the kernel's tail against our head.
	return atomic.LoadUint32(u.cqTail) - *u.cqHead
}

// reap calls fn for up to max completions and advances the head past them.
func (u *uring) reap(max int, fn func(c *cqe)) int {
	head := *u.cqHead
	tail := atomic.LoadUint32(u.cqTail)
	n := 0
	//: oldest first, bounded.
	for head != tail && n < max {
		fn(&u.cqes[head&u.cqMask])
		head++
		n++
	}
	//: the kernel may reuse the entries once the head moved.
	atomic.StoreUint32(u.cqHead, head)
	//: how many.
	return n
}

// overflowed reports a CQ the kernel had to hold back (NODROP): a submit
// flushes it.
func (u *uring) overflowed() bool {
	//: the flag the kernel raises.
	return atomic.LoadUint32(u.sqFlags)&sqCQOverflow != 0
}

// setupBuffers maps count buffers of size bytes and registers their ring as
// group bufGroup. count must be a power of two.
func (u *uring) setupBuffers(count, size uint32) syscall.Errno {
	ringBytes := int(count) * int(unsafe.Sizeof(uringBuf{}))
	rm, err := syscall.Mmap(-1, 0, max(ringBytes, 4096), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	//: no ring memory.
	if err != nil {
		//: refused.
		return errnoOfErr(err)
	}
	u.bufRing = rm
	bm, err := syscall.Mmap(-1, 0, int(count)*int(size), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	//: no buffer memory.
	if err != nil {
		//: refused.
		return errnoOfErr(err)
	}
	u.bufMem, u.bufCount, u.bufSize = bm, count, size
	reg := new(bufReg{ringAddr: uint64(uintptr(unsafe.Pointer(&rm[0]))), ringEntries: count, bgid: bufGroup})
	//: the kernel learns the ring.
	if errno := uringRegister(u.fd, regPbufRing, unsafe.Pointer(reg), 1); errno != 0 {
		//: refused.
		return errno
	}
	//: every buffer starts in the ring, in order.
	for bid := range count {
		u.addBuf(uint16(bid))
	}
	u.publishBufs()
	//: provided.
	return 0
}

// bufEntries views the buffer ring.
func (u *uring) bufEntries() []uringBuf {
	//: count entries of 16 bytes.
	return unsafe.Slice((*uringBuf)(unsafe.Pointer(&u.bufRing[0])), u.bufCount)
}

// buf returns buffer bid's bytes.
func (u *uring) buf(bid uint16) []byte {
	o := int(bid) * int(u.bufSize)
	//: capacity pinned to the buffer.
	return u.bufMem[o : o+int(u.bufSize) : o+int(u.bufSize)]
}

// addBuf writes buffer bid at the ring's next entry, unpublished. Entry 0's
// bid shares its word with the ring's tail, so it is kept in shadowBid and
// written with the tail by publishBufs.
func (u *uring) addBuf(bid uint16) {
	i := uint32(u.bufTail) & (u.bufCount - 1)
	e := &u.bufEntries()[i]
	e.addr = uint64(uintptr(unsafe.Pointer(&u.bufMem[int(bid)*int(u.bufSize)])))
	e.len = u.bufSize
	//: entry 0's bid is published with the tail, in one store.
	if i == 0 {
		u.shadowBid = bid
	} else {
		e.bid = bid
	}
	u.bufTail++
}

// publishBufs makes every added buffer the kernel's: one 32-bit store of
// entry 0's bid and the tail, its last two bytes, after every entry field
// was written. The word is little-endian on every architecture this file
// builds for: bid in the low half, tail in the high one.
func (u *uring) publishBufs() {
	w := (*uint32)(unsafe.Pointer(&u.bufRing[12]))
	atomic.StoreUint32(w, uint32(u.shadowBid)|uint32(u.bufTail)<<16)
}

// registerSparse registers a sparse direct-descriptor table of n entries.
func (u *uring) registerSparse(n uint32) syscall.Errno {
	r := rsrcRegister{nr: n, flags: rsrcRegisterSparse}
	//: one call, sized by the struct.
	return uringRegister(u.fd, regFiles2, unsafe.Pointer(&r), uint32(unsafe.Sizeof(r)))
}

// supports asks REGISTER_PROBE whether every op of ops is supported.
func (u *uring) supports(ops ...uint8) (bool, syscall.Errno) {
	var p probeBuf
	//: the kernel fills what it knows.
	if errno := uringRegister(u.fd, regProbe, unsafe.Pointer(&p), probeOps); errno != 0 {
		//: no probe.
		return false, errno
	}
	//: each op asked about.
	for _, op := range ops {
		//: past what the kernel knows, or not supported.
		if op > p.hdr.lastOp || p.ops[op].flags&probeOpSupported == 0 {
			//: missing.
			return false, 0
		}
	}
	//: all supported.
	return true, 0
}

// prepAccept fills s as a multishot accept on lfd into a kernel-allocated
// direct descriptor.
func prepAccept(s *sqe, lfd int, ud uint64) {
	s.opcode = opAccept
	s.fd = int32(lfd)
	s.ioprio = acceptMultishot
	// No SOCK_CLOEXEC: a direct descriptor is in no process table, and the
	// kernel refuses the flag with a file slot (EINVAL, io_accept_prep).
	s.fileIdx = fileIndexAlloc
	s.userData = ud
}

// prepRecv fills s as a multishot recv on direct descriptor index, selecting
// provided buffers.
func prepRecv(s *sqe, index uint32, ud uint64) {
	s.opcode = opRecv
	s.fd = fixedFd(index)
	s.flags = sqeFixedFile | sqeBufferSelect
	s.ioprio = recvMultishot
	s.bufIndex = bufGroup
	s.userData = ud
}

// prepSend fills s as a send of p on direct descriptor index.
func prepSend(s *sqe, index uint32, p []byte, ud uint64) {
	s.opcode = opSend
	s.fd = fixedFd(index)
	s.flags = sqeFixedFile
	s.addr = uint64(uintptr(unsafe.Pointer(&p[0])))
	s.len = uint32(len(p))
	s.opFlags = uint32(syscall.MSG_NOSIGNAL)
	s.userData = ud
}

// prepCancelFixed fills s as a cancel of every operation on direct descriptor
// index. It is never linked to the close: the close waits for the count.
func prepCancelFixed(s *sqe, index uint32, ud uint64) {
	s.opcode = opAsyncCancel
	s.fd = fixedFd(index)
	s.opFlags = cancelFD | cancelFDFixed | cancelAll
	s.userData = ud
}

// prepCloseDirect fills s as the close of direct descriptor index.
func prepCloseDirect(s *sqe, index uint32, ud uint64) {
	s.opcode = opClose
	s.fileIdx = targetIndex(index)
	s.userData = ud
}

// prepCancelUserData fills s as a cancel of the operation carrying target.
func prepCancelUserData(s *sqe, target, ud uint64) {
	s.opcode = opAsyncCancel
	s.addr = target
	s.userData = ud
}

// prepPollMulti fills s as a multishot POLLIN on a process descriptor.
func prepPollMulti(s *sqe, fd int, ud uint64) {
	s.opcode = opPollAdd
	s.fd = int32(fd)
	s.opFlags = uint32(syscall.EPOLLIN)
	s.len = pollAddMulti
	s.userData = ud
}
