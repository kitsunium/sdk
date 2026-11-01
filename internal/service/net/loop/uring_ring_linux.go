//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	// uringEntries is a serving ring's SQ size; CLAMP lets the kernel lower
	// it, and the size it grants is read back from the params. A ring's SQ and
	// CQ are charged to RLIMIT_MEMLOCK for a process without CAP_IPC_LOCK
	// (an 8 MiB default on most distributions), so they are sized to keep a
	// few rings within it rather than to the largest burst: a full SQ submits
	// inline, a full CQ overflows into the kernel's list (NODROP).
	uringEntries uint32 = 1024
	// probeEntries sizes the probe's short-lived rings.
	probeEntries uint32 = 16
	// cqFactor is how many times uringEntries CQSIZE asks for the CQ.
	cqFactor uint32 = 4
	// bufGroup is the first provided-buffer group a recv selects from; a
	// ring's group g is bufGroup+g.
	bufGroup uint16 = 1
	// defaultBufs is the fewest provided buffers a ring may hand the kernel,
	// and the entries of each of its buffer rings. They are mapped at setup
	// and handed out as the ring needs them (uRing.provide): an untouched
	// buffer is no resident memory, so a ring's buffers cost the most it
	// ever had out at once, not its count (ADR 0201 §D1).
	defaultBufs uint32 = 8192
	// maxBufs is the most provided buffers a ring may hand the kernel: a
	// bid is 16 bits. A ring may have as many as its table holds
	// connections, a power of two, in groups of defaultBufs registered as
	// they are first needed (ADR 0203 §D2).
	maxBufs uint32 = 1 << 16
	// defaultBufSize is the size of one provided buffer, in bytes: an HTTP
	// request of a keep-alive connection fits in one; a larger one arrives
	// over several completions, as a split read always could.
	defaultBufSize uint32 = 2048
	// bufsFirst is how many buffers a ring hands the kernel at setup.
	bufsFirst uint32 = 512
	// bufsStep is the fewest more it hands each time it runs short.
	bufsStep uint32 = 512
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
	// bufMem is every buffer's mapping, by bid; bufCount how many it holds,
	// bufSize the bytes of one, bufPer the entries of one group's ring.
	bufMem   []byte
	bufCount uint32
	bufSize  uint32
	bufPer   uint32
	// groups are the provided-buffer groups registered so far: group g
	// holds bids g*bufPer to (g+1)*bufPer-1 and is registered as
	// bufGroup+g, once its first bid is handed out.
	groups []pbufGroup
	// bufProvided is how many buffers, bids 0 to bufProvided-1, the kernel
	// has been given so far; the others were never touched.
	bufProvided uint32
	// retries counts busy submits retried.
	retries uint64
	// setup is the flags the ring was set up with, as asked: what the kernel
	// writes back into params is not read for them.
	setup uint32
	// ringIdx is the ring's index among its thread's registered rings
	// (IORING_REGISTER_RING_FDS), -1 when it is entered by descriptor.
	ringIdx int32
	// taken sees every SQE the kernel took, on the ring's thread; nil but in
	// the stop tests (loopHooks.taken).
	taken func(op, flags uint8, opFlags uint32)
}

// pbufGroup is one provided-buffer group: its ring's mapping, its tail, and
// entry 0's bid, written with the tail; dirty is a buffer added since the
// tail was last published.
type pbufGroup struct {
	ring      []byte
	tail      uint16
	shadowBid uint16
	dirty     bool
}

// openURing sets an io_uring of entries SQEs up with flags, mapping it and building the
// views. It returns the errno of the step that failed.
func openURing(entries, flags uint32) (*uring, string, syscall.Errno) {
	u := &uring{fd: -1, ringIdx: -1}
	u.params.flags = flags | setupCQSize | setupClamp
	u.params.cqEntries = entries * cqFactor
	fd, errno := uringSetup(entries, &u.params)
	//: the kernel refused the setup.
	if errno != 0 {
		//: the caller decides whether to retry with fewer flags.
		return nil, "setup", errno
	}
	u.fd = fd
	u.setup = flags
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
	//: without NO_SQARRAY, the SQ array maps slot i to SQE i once and for all.
	if !u.noArray() {
		array := unsafe.Slice((*uint32)(unsafe.Add(base, p.sqOff.array)), p.sqEntries)
		//: identity.
		for i := range array {
			array[i] = uint32(i)
		}
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

// close releases every mapping and the ring, its registration with the
// thread first: a ring still registered stays referenced by the thread and
// outlives its close, its direct descriptors with it (P3c).
func (u *uring) close() {
	u.unregisterRing()
	//: each mapping that exists.
	maps := [][]byte{u.bufMem, u.sqeMem, u.ringMem}
	//: each group's ring.
	for _, g := range u.groups {
		maps = append(maps, g.ring)
	}
	//: each mapping that exists.
	for _, m := range maps {
		//: mapped.
		if m != nil {
			swallowErr(syscall.Munmap(m))
		}
	}
	u.groups, u.bufMem, u.sqeMem, u.ringMem = nil, nil, nil, nil
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
	for i := 0; !u.room(1); i++ {
		//: the kernel never drained the SQ.
		if i >= enterRetries {
			//: the caller counts the failure.
			return nil
		}
		u.submit()
	}
	s := u.slot()
	*s = sqe{}
	u.tail++
	//: the caller fills it; publish makes it the kernel's.
	return s
}

// noArray is NO_SQARRAY: there is no SQ array to fill.
func (u *uring) noArray() bool {
	//: as set up.
	return u.setup&setupNoSQArray != 0
}

// rewind is SQ_REWIND: the kernel takes every submission from SQE 0, so the
// pending SQEs are always sqes[0:pending] (uring.slot).
func (u *uring) rewind() bool {
	//: as set up.
	return u.setup&setupSQRewind != 0
}

// deferred is DEFER_TASKRUN: completions are posted only when this thread
// enters with GETEVENTS, which every submit then does with min_complete 0.
func (u *uring) deferred() bool {
	//: as set up.
	return u.setup&setupDeferTaskrun != 0
}

// room reports whether n more SQEs fit: against the kernel's head, or,
// under SQ_REWIND, against the SQEs filled since the last submit.
func (u *uring) room(n uint32) bool {
	//: the head and tail stay zero under SQ_REWIND.
	if u.rewind() {
		//: what is pending is all there is.
		return u.pending()+n <= u.sqLen
	}
	//: what the kernel has not consumed yet.
	return u.tail-atomic.LoadUint32(u.sqHead)+n <= u.sqLen
}

// slot is the SQE the next get fills: the tail's, or, under SQ_REWIND, the
// one after those pending, the submitter filling from SQE 0.
func (u *uring) slot() *sqe {
	//: from 0.
	if u.rewind() {
		//: after the pending ones.
		return &u.sqes[u.pending()]
	}
	//: the tail's.
	return &u.sqes[u.tail&u.sqMask]
}

// consumed records got SQEs taken by the kernel. Under SQ_REWIND a partial
// submit leaves the rest at sqes[got:pending], and the kernel starts at 0
// again: they are moved down, in order, links and all.
func (u *uring) consumed(got uint32) {
	//: a stop test counts what the kernel took, before a rewind moves it.
	if u.taken != nil {
		u.see(got)
	}
	//: the kernel took fewer than were pending.
	if u.rewind() && got < u.pending() {
		copy(u.sqes[:u.pending()-got], u.sqes[got:u.pending()])
	}
	u.submitted += got
}

// see hands the got SQEs the kernel just took to taken, oldest first: from
// SQE 0 under SQ_REWIND, from the last submitted tail otherwise.
func (u *uring) see(got uint32) {
	//: in the order they were filled.
	for i := range got {
		s := &u.sqes[(u.submitted+i)&u.sqMask]
		//: the rewind's are where its submit started.
		if u.rewind() {
			s = &u.sqes[i]
		}
		u.taken(s.opcode, s.flags, s.opFlags)
	}
}

// publish hands every filled SQE to the kernel's view of the SQ tail; under
// SQ_REWIND the tail stays zero and the count passed to enter is the view.
func (u *uring) publish() {
	//: no tail to move.
	if u.rewind() {
		//: nothing.
		return
	}
	//: the SQEs are written before the tail moves.
	atomic.StoreUint32(u.sqTail, u.tail)
}

// enterSubmit submits n SQEs: the plain submit, which inlines, when no
// flag is asked for, one with flags otherwise.
func (u *uring) enterSubmit(fd int, n, flags uint32) (int, syscall.Errno) {
	//: nothing else asked.
	if flags == 0 {
		//: plain.
		return uringEnterSubmit(fd, n)
	}
	//: with flags.
	return uringEnterFlags(fd, n, flags)
}

// enterTarget is the fd argument and the flag of every enter: the
// registered index when the ring has one, the descriptor otherwise.
func (u *uring) enterTarget() (int, uint32) {
	//: registered with the thread.
	if u.ringIdx >= 0 {
		//: the index.
		return int(u.ringIdx), enterRegisteredRing
	}
	//: the descriptor.
	return u.fd, 0
}

// registerRing registers the ring with the calling thread
// (IORING_REGISTER_RING_FDS), the kernel picking the index. It must run on
// the thread that enters it, and unregisterRing on the same one.
func (u *uring) registerRing() syscall.Errno {
	upd := new(rsrcUpdate{offset: ringFDAlloc, data: uint64(u.fd)})
	errno := uringRegister(u.fd, regRingFDs, unsafe.Pointer(upd), 1)
	//: refused.
	if errno != 0 {
		//: as answered.
		return errno
	}
	u.ringIdx = int32(upd.offset)
	//: registered.
	return 0
}

// unregisterRing drops the thread's registration, if any.
func (u *uring) unregisterRing() {
	//: none.
	if u.ringIdx < 0 || u.fd < 0 {
		//: nothing.
		return
	}
	upd := new(rsrcUpdate{offset: uint32(u.ringIdx)})
	swallowErr(uringRegister(u.fd, unregRingFDs, unsafe.Pointer(upd), 1))
	u.ringIdx = -1
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
		fd, reg := u.enterTarget()
		flags := reg
		//: DEFER_TASKRUN posts completions only on GETEVENTS, from this thread.
		if u.deferred() {
			flags |= enterGetEvents
		}
		got, errno := u.enterSubmit(fd, n, flags)
		//: by outcome.
		switch errno {
		//: taken, some or all.
		case 0:
			u.consumed(uint32(got))
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

// wait submits what is pending and blocks until want completions or the
// timeout, in one io_uring_enter with EXT_ARG. ETIME is the tick, or the
// grouped wait's bound (ADR 0199). The kernel wakes the thread once the CQ
// holds want entries past its head, or when ts runs out, and an ETIME with
// completions in the CQ returns zero (io_cqring_wait, io_uring/io_uring.c,
// 6.8 to 7.0); either way the caller reaps what came. A completion already in
// the CQ makes it a submit, with no scheduler round trip, and no system call
// at all when nothing is pending (ADR 0202).
func (u *uring) wait(ts *kernelTimespec, want uint32) {
	//: a completion already there: the caller reaps it now.
	if u.ready() > 0 {
		u.submit()
		//: submitted, or nothing to.
		return
	}
	u.publish()
	arg := getEventsArg{ts: uint64(uintptr(unsafe.Pointer(ts)))}
	fd, reg := u.enterTarget()
	got, errno := uringEnterWait(fd, u.pending(), max(want, 1), enterGetEvents|enterExtArg|reg, unsafe.Pointer(&arg), unsafe.Sizeof(arg))
	//: by outcome; the CQ is peeked by the caller whatever happened.
	switch errno {
	//: submitted, and woken.
	case 0:
		u.consumed(uint32(got))
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

// overflowed reports a CQ the kernel had to hold back (NODROP):
// flushOverflow moves it into the CQ.
func (u *uring) overflowed() bool {
	//: the flag the kernel raises.
	return atomic.LoadUint32(u.sqFlags)&sqCQOverflow != 0
}

// flushOverflow submits what is pending and enters with GETEVENTS and
// min_complete zero, which never blocks: the kernel moves the completions it
// held back into the CQ only on its wait path (io_cqring_wait, 6.8 to 7.0),
// never on a plain submit. A secondary ring is otherwise entered only to
// submit, so a CQ that overflowed there stayed held — every close of a
// drain on that ring waiting on a completion it never saw.
func (u *uring) flushOverflow() {
	u.publish()
	fd, reg := u.enterTarget()
	got, errno := uringEnterFlags(fd, u.pending(), enterGetEvents|reg)
	//: by outcome; the caller reaps what came.
	switch errno {
	//: submitted, and flushed.
	case 0:
		u.consumed(uint32(got))
	//: interrupted: the next reap tries again.
	case syscall.EINTR:
	default:
		u.retries++
	}
}

// setupBuffers maps count buffers of size bytes, registers the ring of their
// first group, and hands the kernel the first first of them; growBufs hands
// it the others, registering each further group as its first bid goes out.
// count must be a power of two, at most maxBufs; a group holds defaultBufs
// of them, or count when that is fewer. The mapping is private and
// anonymous, so a buffer the kernel was never given is no resident page, and
// a group never needed costs no kernel memory.
func (u *uring) setupBuffers(count, size, first uint32) syscall.Errno {
	bm, err := syscall.Mmap(-1, 0, int(count)*int(size), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	//: no buffer memory.
	if err != nil {
		//: refused.
		return errnoOfErr(err)
	}
	u.bufMem, u.bufCount, u.bufSize, u.bufPer = bm, count, size, min(count, defaultBufs)
	//: the first group's ring: the kernel's own pages first, user pages
	//: before 6.4.
	if errno := u.addGroup(); errno != 0 {
		//: refused.
		return errno
	}
	u.growBufs(min(first, count))
	//: provided.
	return 0
}

// growBufs hands the kernel up to n buffers it never had, in bid order,
// published once, and says how many. Every bid is in its group's ring at most
// once, so a ring never holds more than its entries. A group whose ring the
// kernel refuses ends the growth there: the ring keeps the groups it has.
func (u *uring) growBufs(n uint32) uint32 {
	n = min(n, u.bufCount-u.bufProvided)
	end := u.bufProvided + n
	bid := u.bufProvided
	//: each new bid, at its group's tail; a new group registered first.
	for ; bid < end; bid++ {
		//: the group's first bid: its ring is registered now.
		if bid%u.bufPer == 0 && int(bid/u.bufPer) == len(u.groups) && u.addGroup() != 0 {
			u.bufCount = bid
			break
		}
		u.addBuf(uint16(bid))
	}
	n = bid - u.bufProvided
	u.bufProvided = bid
	u.publishBufs()
	//: how many.
	return n
}

// addGroup registers the ring of the next group and maps it.
func (u *uring) addGroup() syscall.Errno {
	g := uint16(len(u.groups))
	rm, errno := u.mapBufRing(bufGroup+g, u.bufPer)
	//: refused.
	if errno != 0 {
		//: as answered.
		return errno
	}
	u.groups = append(u.groups, pbufGroup{ring: rm})
	//: registered.
	return 0
}

// mapBufRing registers group bgid's ring of count entries and maps it.
// It asks the kernel to allocate it (IOU_PBUF_RING_MMAP, 6.4): a ring in user
// pages is pinned and charged to RLIMIT_MEMLOCK, which an unprivileged
// process runs out of after a few rings (CI's non-root lane did). A kernel
// that does not know the flag (EINVAL) gets the ring in user pages.
func (u *uring) mapBufRing(bgid uint16, count uint32) ([]byte, syscall.Errno) {
	ringBytes := max(int(count)*int(unsafe.Sizeof(uringBuf{})), syscall.Getpagesize())
	errno := registerBufRing(u.fd, &bufReg{ringEntries: count, bgid: bgid, flags: pbufRingMmap})
	//: the kernel's pages: mapped at the group's offset.
	if errno == 0 {
		off := offPbufRing | uintptr(bgid)<<offPbufShift
		rm, err := syscall.Mmap(u.fd, int64(off), ringBytes, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED|syscall.MAP_POPULATE)
		//: registered but not mappable.
		if err != nil {
			//: refused.
			return nil, errnoOfErr(err)
		}
		//: mapped.
		return rm, 0
	}
	//: a refusal other than an unknown flag is final.
	if errno != syscall.EINVAL {
		//: refused.
		return nil, errno
	}
	rm, err := syscall.Mmap(-1, 0, ringBytes, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	//: no ring memory.
	if err != nil {
		//: refused.
		return nil, errnoOfErr(err)
	}
	//: the kernel learns the ring in user pages.
	if errno := registerBufRing(u.fd, &bufReg{ringAddr: uint64(uintptr(unsafe.Pointer(&rm[0]))), ringEntries: count, bgid: bgid}); errno != 0 {
		swallowErr(syscall.Munmap(rm))
		//: refused.
		return nil, errno
	}
	//: registered.
	return rm, 0
}

// registerBufRing is IORING_REGISTER_PBUF_RING with reg, resv zero as the
// uapi requires. Ubuntu's noble kernel 6.8.0-147 inverts that check
// (io_uring/kbuf.c: `if (!memchr_inv(reg.resv, 0, sizeof(reg.resv)))
// return -EINVAL;`, Ubuntu-6.8.0-147.147) and refuses every zero resv; on
// EINVAL the registration is tried once more with one resv byte set, which
// that kernel accepts and a correct one refuses again with EINVAL.
func registerBufRing(fd int, reg *bufReg) syscall.Errno {
	errno := uringRegister(fd, regPbufRing, unsafe.Pointer(reg), 1)
	//: accepted, or refused for another reason.
	if errno != syscall.EINVAL {
		//: as answered.
		return errno
	}
	quirk := *reg
	quirk.resv[2] = 1
	//: the inverted check's one way through.
	return uringRegister(fd, regPbufRing, unsafe.Pointer(&quirk), 1)
}

// bufEntries views group g's buffer ring.
func (u *uring) bufEntries(g int) []uringBuf {
	//: bufPer entries of 16 bytes.
	return unsafe.Slice((*uringBuf)(unsafe.Pointer(&u.groups[g].ring[0])), u.bufPer)
}

// groupOf is the group buffer bid belongs to.
func (u *uring) groupOf(bid uint16) int {
	//: by its place in the mapping.
	return int(uint32(bid) / u.bufPer)
}

// buf returns buffer bid's bytes.
func (u *uring) buf(bid uint16) []byte {
	o := int(bid) * int(u.bufSize)
	//: capacity pinned to the buffer.
	return u.bufMem[o : o+int(u.bufSize) : o+int(u.bufSize)]
}

// addBuf writes buffer bid at its group's ring's next entry, unpublished.
// Entry 0's bid shares its word with the ring's tail, so it is kept in
// shadowBid and written with the tail by publishBufs.
func (u *uring) addBuf(bid uint16) {
	g := &u.groups[u.groupOf(bid)]
	i := uint32(g.tail) & (u.bufPer - 1)
	e := &u.bufEntries(u.groupOf(bid))[i]
	e.addr = uint64(uintptr(unsafe.Pointer(&u.bufMem[int(bid)*int(u.bufSize)])))
	e.len = u.bufSize
	//: entry 0's bid is published with the tail, in one store.
	if i == 0 {
		g.shadowBid = bid
	} else {
		e.bid = bid
	}
	g.tail++
	g.dirty = true
}

// publishBufs makes every added buffer the kernel's: for each group with
// one, a 32-bit store of entry 0's bid and the tail, its ring's last two
// bytes, after every entry field was written. The word is little-endian on
// every architecture this file builds for: bid in the low half, tail in the
// high one.
func (u *uring) publishBufs() {
	//: each group a buffer was added to.
	for i := range u.groups {
		g := &u.groups[i]
		//: nothing added.
		if !g.dirty {
			continue
		}
		w := (*uint32)(unsafe.Pointer(&g.ring[12]))
		atomic.StoreUint32(w, uint32(g.shadowBid)|uint32(g.tail)<<16)
		g.dirty = false
	}
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

// singleShot turns a multishot recv into a single-shot one: one
// completion, never F_MORE, re-armed by the state machine after it.
func singleShot(s *sqe) {
	s.ioprio &^= recvMultishot
}

// prepAccept fills s as an accept on lfd into a kernel-allocated direct
// descriptor: multishot for the probe's self-test, single-shot for serving
// (uRing.armAccepts says why).
func prepAccept(s *sqe, lfd int, ud uint64, multishot bool) {
	s.opcode = opAccept
	s.fd = int32(lfd)
	//: one completion per connection until cancelled, or exactly one.
	if multishot {
		s.ioprio = acceptMultishot
	}
	// No SOCK_CLOEXEC: a direct descriptor is in no process table, and the
	// kernel refuses the flag with a file slot (EINVAL, io_accept_prep).
	s.fileIdx = fileIndexAlloc
	s.userData = ud
}

// prepRecv fills s as a multishot recv on direct descriptor index, selecting
// provided buffers from group bgid.
func prepRecv(s *sqe, index uint32, bgid uint16, ud uint64) {
	s.opcode = opRecv
	s.fd = fixedFd(index)
	s.flags = sqeFixedFile | sqeBufferSelect
	s.ioprio = recvMultishot
	s.bufIndex = bgid
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
// index. Only the probe's release check uses it, on a ring of a few
// requests: the kernel finds them by walking every armed request
// (io_poll_file_find), which a serving ring cannot afford at each close.
func prepCancelFixed(s *sqe, index uint32, ud uint64) {
	s.opcode = opAsyncCancel
	s.fd = fixedFd(index)
	s.opFlags = cancelFD | cancelFDFixed | cancelAll
	s.userData = ud
}

// prepShutdownFixed fills s as shutdown(2) with how on direct descriptor
// index. It sends the FIN without waiting for the file to be put, which a
// direct descriptor's close may defer (uRing.shutdown says why).
func prepShutdownFixed(s *sqe, index uint32, how int, ud uint64) {
	s.opcode = opShutdown
	s.fd = fixedFd(index)
	s.flags = sqeFixedFile
	s.len = uint32(how)
	s.userData = ud
}

// onProcessFD turns an SQE filled for a direct descriptor into the same
// operation on the process descriptor of the same number: no fixed-file
// flag, a cancel that looks the number up in the process table, an accept
// that installs into it, close-on-exec.
func onProcessFD(s *sqe) {
	s.flags &^= sqeFixedFile
	//: by operation.
	switch s.opcode {
	//: into the process table.
	case opAccept:
		s.fileIdx = 0
		s.opFlags = syscall.SOCK_CLOEXEC
	//: the number is a process descriptor.
	case opAsyncCancel:
		s.opFlags &^= cancelFDFixed
	}
}

// prepCloseDirect fills s as the close of direct descriptor index.
func prepCloseDirect(s *sqe, index uint32, ud uint64) {
	s.opcode = opClose
	s.fileIdx = targetIndex(index)
	s.userData = ud
}

// prepCancelUserData fills s as a cancel of every operation carrying target.
func prepCancelUserData(s *sqe, target, ud uint64) {
	s.opcode = opAsyncCancel
	s.addr = target
	s.opFlags = cancelAll
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
