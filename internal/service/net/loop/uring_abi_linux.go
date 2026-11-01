//go:build linux && (amd64 || arm64 || riscv64 || loong64)

package loop

import (
	"syscall"
	"unsafe"
)

// The io_uring ABI this backend speaks, declared in-tree because the standard
// library's syscall package has none of it and golang.org/x/sys is banned
// (ADR 0018). Every number and layout is Linux include/uapi/linux/io_uring.h
// as of 6.8, and the layouts are frozen by uring_abi_linux_internal_test.go's
// Sizeof and Offsetof checks. The four architectures share the generic syscall
// table, where io_uring has had the same numbers since 5.1.
const (
	sysIOURingSetup    uintptr = 425
	sysIOURingEnter    uintptr = 426
	sysIOURingRegister uintptr = 427

	// io_uring_setup flags.
	setupCQSize       uint32 = 1 << 3
	setupClamp        uint32 = 1 << 4
	setupSubmitAll    uint32 = 1 << 7
	setupCoopTaskrun  uint32 = 1 << 8
	setupSingleIssuer uint32 = 1 << 12
	setupDeferTaskrun uint32 = 1 << 13
	// setupNoSQArray is IORING_SETUP_NO_SQARRAY (6.6): the SQ tail indexes
	// the SQEs directly and sqOff.array reads zero.
	setupNoSQArray uint32 = 1 << 16
	// setupSQRewind is IORING_SETUP_SQ_REWIND (7.0): the kernel ignores the
	// SQ head and tail and takes every submission from SQE 0. It requires
	// NO_SQARRAY and refuses SQPOLL, which this backend never sets.
	setupSQRewind uint32 = 1 << 20

	// io_uring_params.features.
	featSingleMmap uint32 = 1 << 0
	featNoDrop     uint32 = 1 << 1
	featExtArg     uint32 = 1 << 8
	// featMinTimeout is IORING_FEAT_MIN_TIMEOUT (6.12), class B's min_wait.
	featMinTimeout uint32 = 1 << 15

	// io_uring_enter flags.
	enterGetEvents uint32 = 1 << 0
	enterExtArg    uint32 = 1 << 3
	// enterRegisteredRing is IORING_ENTER_REGISTERED_RING: fd is the index
	// IORING_REGISTER_RING_FDS returned, not a descriptor.
	enterRegisteredRing uint32 = 1 << 4

	// SQ ring flags.
	sqCQOverflow uint32 = 1 << 1

	// mmap offsets.
	offSQRing uintptr = 0
	offCQRing uintptr = 0x8000000
	offSQEs   uintptr = 0x10000000

	// Opcodes.
	opNop         uint8 = 0
	opPollAdd     uint8 = 6
	opAccept      uint8 = 13
	opAsyncCancel uint8 = 14
	opClose       uint8 = 19
	opRead        uint8 = 22
	opSend        uint8 = 26
	opRecv        uint8 = 27
	opShutdown    uint8 = 34

	// SQE flags.
	sqeFixedFile uint8 = 1 << 0
	// sqeIOHardlink is IOSQE_IO_HARDLINK: the next SQE starts once this one
	// completed, whatever its result.
	sqeIOHardlink   uint8 = 1 << 3
	sqeAsync        uint8 = 1 << 4
	sqeBufferSelect uint8 = 1 << 5

	// ioprio flags of accept and recv.
	acceptMultishot uint16 = 1 << 0
	recvMultishot   uint16 = 1 << 1

	// len flag of POLL_ADD: a multishot poll.
	pollAddMulti uint32 = 1 << 0

	// Cancel flags.
	cancelAll     uint32 = 1 << 0
	cancelFD      uint32 = 1 << 1
	cancelFDFixed uint32 = 1 << 3

	// CQE flags.
	cqeFBuffer uint32 = 1 << 0
	cqeFMore   uint32 = 1 << 1
	// cqeFBufMore is IORING_CQE_F_BUF_MORE (6.12): the completion's buffer
	// stays the kernel's, partly consumed, for more completions.
	cqeFBufMore    uint32 = 1 << 4
	cqeBufferShift uint32 = 16

	// fileIndexAlloc is IORING_FILE_INDEX_ALLOC, (~0U): the kernel picks a
	// free slot of the direct-descriptor table and returns it in res.
	fileIndexAlloc uint32 = 0xFFFFFFFF

	// io_uring_register opcodes.
	regEventFD    uintptr = 4
	regProbe      uintptr = 8
	regFiles2     uintptr = 13
	regRingFDs    uintptr = 20
	unregRingFDs  uintptr = 21
	regPbufRing   uintptr = 22
	unregPbufRing uintptr = 23
	// ringFDsMax is IO_RINGFD_REG_MAX: a thread registers at most 16 rings.
	ringFDsMax int = 16
	// ringFDAlloc is the offset -1U that lets the kernel pick the index.
	ringFDAlloc uint32 = 0xFFFFFFFF
	// pbufRingMmap is IOU_PBUF_RING_MMAP (6.4): the kernel allocates the
	// buffer ring and the loop maps it, at offPbufRing | bgid << offPbufShift,
	// so no user page is pinned against RLIMIT_MEMLOCK.
	pbufRingMmap uint16 = 1
	// pbufRingInc is IOU_PBUF_RING_INC (6.12): a buffer is consumed as far
	// as each completion took, and serves the next from where it stopped.
	pbufRingInc  uint16  = 2
	offPbufRing  uintptr = 0x80000000
	offPbufShift uintptr = 16

	// rsrcRegisterSparse is IORING_RSRC_REGISTER_SPARSE.
	rsrcRegisterSparse uint32 = 1 << 0

	// probeOpSupported is IO_URING_OP_SUPPORTED.
	probeOpSupported uint16 = 1 << 0
	// probeOps is how many entries the probe asks about.
	probeOps uint32 = 64

	// errnoETIME and the others the enter loop branches on.
	errnoETIME                   = syscall.Errno(62)
	errnoECANCELED               = syscall.Errno(125)
	errnoENOBUFS   syscall.Errno = syscall.ENOBUFS
)

// sqringOffsets is struct io_sqring_offsets.
type sqringOffsets struct {
	head, tail, ringMask, ringEntries, flags, dropped, array, resv1 uint32
	userAddr                                                        uint64
}

// cqringOffsets is struct io_cqring_offsets.
type cqringOffsets struct {
	head, tail, ringMask, ringEntries, overflow, cqes, flags, resv1 uint32
	userAddr                                                        uint64
}

// uringParams is struct io_uring_params.
type uringParams struct {
	sqEntries, cqEntries, flags, sqThreadCPU, sqThreadIdle, features, wqFD uint32
	resv                                                                   [3]uint32
	sqOff                                                                  sqringOffsets
	cqOff                                                                  cqringOffsets
}

// sqe is struct io_uring_sqe, 64 bytes; the unions are named by what this
// backend puts in them.
type sqe struct {
	opcode   uint8
	flags    uint8
	ioprio   uint16
	fd       int32
	off      uint64
	addr     uint64
	len      uint32
	opFlags  uint32
	userData uint64
	bufIndex uint16
	persona  uint16
	fileIdx  uint32
	addr3    uint64
	pad2     uint64
}

// cqe is struct io_uring_cqe, 16 bytes.
type cqe struct {
	userData uint64
	res      int32
	flags    uint32
}

// uringBuf is struct io_uring_buf, 16 bytes; in the buffer ring's first entry
// the last two bytes are the ring's tail.
type uringBuf struct {
	addr uint64
	len  uint32
	bid  uint16
	resv uint16
}

// bufReg is struct io_uring_buf_reg.
type bufReg struct {
	ringAddr    uint64
	ringEntries uint32
	bgid        uint16
	flags       uint16
	resv        [3]uint64
}

// rsrcRegister is struct io_uring_rsrc_register.
type rsrcRegister struct {
	nr    uint32
	flags uint32
	resv2 uint64
	data  uint64
	tags  uint64
}

// rsrcUpdate is struct io_uring_rsrc_update, IORING_REGISTER_RING_FDS's
// argument: offset is the index, data the ring's descriptor.
type rsrcUpdate struct {
	offset uint32
	resv   uint32
	data   uint64
}

// getEventsArg is struct io_uring_getevents_arg.
type getEventsArg struct {
	sigmask   uint64
	sigmaskSz uint32
	pad       uint32
	ts        uint64
}

// kernelTimespec is struct __kernel_timespec.
type kernelTimespec struct {
	sec  int64
	nsec int64
}

// probeHeader is struct io_uring_probe without its flexible array.
type probeHeader struct {
	lastOp uint8
	opsLen uint8
	resv   uint16
	resv2  [3]uint32
}

// probeOp is struct io_uring_probe_op.
type probeOp struct {
	op    uint8
	resv  uint8
	flags uint16
	resv2 uint32
}

// probeBuf is a probe with room for probeOps entries.
type probeBuf struct {
	hdr probeHeader
	ops [probeOps]probeOp
}

// uringSetup is io_uring_setup(2).
func uringSetup(entries uint32, p *uringParams) (int, syscall.Errno) {
	fd, _, errno := syscall.RawSyscall(sysIOURingSetup, uintptr(entries), uintptr(unsafe.Pointer(p)), 0)
	//: the ring's descriptor, or the kernel's refusal.
	return int(fd), errno
}

// uringRegister is io_uring_register(2).
func uringRegister(fd int, op uintptr, arg unsafe.Pointer, n uint32) syscall.Errno {
	_, _, errno := syscall.RawSyscall6(sysIOURingRegister, uintptr(fd), op, uintptr(arg), uintptr(n), 0, 0)
	//: zero on success.
	return errno
}

// uringEnterWait is io_uring_enter(2) that may block: one Syscall6, so the
// runtime knows the thread is in the kernel, per batch.
func uringEnterWait(fd int, submit, minComplete, flags uint32, arg unsafe.Pointer, argSz uintptr) (int, syscall.Errno) {
	n, _, errno := syscall.Syscall6(sysIOURingEnter, uintptr(fd), uintptr(submit), uintptr(minComplete), uintptr(flags), uintptr(arg), argSz)
	//: the SQEs consumed, or the kernel's error.
	return int(n), errno
}

// uringEnterSubmit is io_uring_enter(2) that only submits and never blocks:
// RawSyscall6, with no scheduler round trip.
func uringEnterSubmit(fd int, submit uint32) (int, syscall.Errno) {
	n, _, errno := syscall.RawSyscall6(sysIOURingEnter, uintptr(fd), uintptr(submit), 0, 0, 0, 0)
	//: the SQEs consumed, or the kernel's error.
	return int(n), errno
}

// uringEnterFlags is uringEnterSubmit with flags: GETEVENTS with
// min_complete zero, which never blocks, and REGISTERED_RING.
func uringEnterFlags(fd int, submit, flags uint32) (int, syscall.Errno) {
	n, _, errno := syscall.RawSyscall6(sysIOURingEnter, uintptr(fd), uintptr(submit), 0, uintptr(flags), 0, 0)
	//: the SQEs consumed, or the kernel's error.
	return int(n), errno
}

// fixedFd is the descriptor field of an SQE flagged IOSQE_FIXED_FILE: the
// direct-descriptor index itself, 0-based.
func fixedFd(index uint32) int32 {
	//: the index, as is.
	return int32(index)
}

// targetIndex is the file_index field that names a direct descriptor for
// IORING_OP_CLOSE and for an accept into a chosen index: the index plus one,
// zero meaning "not a direct descriptor".
func targetIndex(index uint32) uint32 {
	//: one-based.
	return index + 1
}
