//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — the in-tree io_uring ABI, frozen against the 6.8 uapi.
package loop

import (
	"encoding/binary"
	"syscall"
	"testing"
	"unsafe"
)

// TestURingABI pins every layout this backend shares with the kernel against
// include/uapi/linux/io_uring.h (6.8): a field that moved would make the
// kernel read another one, silently.
func TestURingABI(t *testing.T) {
	type tc struct {
		name string
		got  uintptr
		want uintptr
	}
	var s sqe
	var c cqe
	var b uringBuf
	var p uringParams
	var r bufReg
	var rs rsrcRegister
	var ga getEventsArg
	var ru rsrcUpdate
	tests := []tc{
		{"sizeof(io_uring_sqe)", unsafe.Sizeof(s), 64},
		{"sqe.opcode", unsafe.Offsetof(s.opcode), 0},
		{"sqe.flags", unsafe.Offsetof(s.flags), 1},
		{"sqe.ioprio", unsafe.Offsetof(s.ioprio), 2},
		{"sqe.fd", unsafe.Offsetof(s.fd), 4},
		{"sqe.off", unsafe.Offsetof(s.off), 8},
		{"sqe.addr", unsafe.Offsetof(s.addr), 16},
		{"sqe.len", unsafe.Offsetof(s.len), 24},
		{"sqe.op_flags", unsafe.Offsetof(s.opFlags), 28},
		{"sqe.user_data", unsafe.Offsetof(s.userData), 32},
		{"sqe.buf_index", unsafe.Offsetof(s.bufIndex), 40},
		{"sqe.personality", unsafe.Offsetof(s.persona), 42},
		{"sqe.file_index", unsafe.Offsetof(s.fileIdx), 44},
		{"sqe.addr3", unsafe.Offsetof(s.addr3), 48},
		{"sizeof(io_uring_cqe)", unsafe.Sizeof(c), 16},
		{"cqe.res", unsafe.Offsetof(c.res), 8},
		{"cqe.flags", unsafe.Offsetof(c.flags), 12},
		{"sizeof(io_uring_buf)", unsafe.Sizeof(b), 16},
		{"buf.bid", unsafe.Offsetof(b.bid), 12},
		{"buf ring tail (resv)", unsafe.Offsetof(b.resv), 14},
		{"sizeof(io_uring_params)", unsafe.Sizeof(p), 120},
		{"params.sq_off", unsafe.Offsetof(p.sqOff), 40},
		{"params.cq_off", unsafe.Offsetof(p.cqOff), 80},
		{"sizeof(io_sqring_offsets)", unsafe.Sizeof(p.sqOff), 40},
		{"sizeof(io_cqring_offsets)", unsafe.Sizeof(p.cqOff), 40},
		{"sizeof(io_uring_buf_reg)", unsafe.Sizeof(r), 40},
		{"sizeof(io_uring_rsrc_register)", unsafe.Sizeof(rs), 32},
		{"sizeof(io_uring_getevents_arg)", unsafe.Sizeof(ga), 24},
		{"sizeof(io_uring_rsrc_update)", unsafe.Sizeof(ru), 16},
		{"rsrc_update.data", unsafe.Offsetof(ru.data), 8},
		{"sizeof(io_uring_probe)", unsafe.Sizeof(probeHeader{}), 16},
		{"sizeof(io_uring_probe_op)", unsafe.Sizeof(probeOp{}), 8},
	}
	for _, c := range tests {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if fileIndexAlloc != ^uint32(0) {
		t.Errorf("IORING_FILE_INDEX_ALLOC = %#x, want ~0U", fileIndexAlloc)
	}
	// The class A-code numbers, against the uapi of 7.0.
	for _, f := range []struct {
		name      string
		got, want uint64
	}{
		{"IORING_SETUP_DEFER_TASKRUN", uint64(setupDeferTaskrun), 1 << 13},
		{"IORING_SETUP_NO_SQARRAY", uint64(setupNoSQArray), 1 << 16},
		{"IORING_SETUP_SQ_REWIND", uint64(setupSQRewind), 1 << 20},
		{"IORING_ENTER_REGISTERED_RING", uint64(enterRegisteredRing), 1 << 4},
		{"IORING_FEAT_MIN_TIMEOUT", uint64(featMinTimeout), 1 << 15},
		{"IORING_REGISTER_RING_FDS", uint64(regRingFDs), 20},
		{"IORING_UNREGISTER_RING_FDS", uint64(unregRingFDs), 21},
		{"IO_RINGFD_REG_MAX", uint64(ringFDsMax), 16},
	} {
		if f.got != f.want {
			t.Errorf("%s = %#x, want %#x", f.name, f.got, f.want)
		}
	}
}

// TestURingEncoders compares the SQEs this backend writes, byte for byte,
// with what liburing's helpers write for the same call (liburing 2.5,
// io_uring_prep_multishot_accept_direct, io_uring_prep_recv_multishot with
// IOSQE_FIXED_FILE|IOSQE_BUFFER_SELECT, io_uring_prep_send,
// io_uring_prep_close_direct, io_uring_prep_cancel_fd with
// IORING_ASYNC_CANCEL_FD_FIXED|ALL, io_uring_prep_shutdown with
// IOSQE_FIXED_FILE): the fixed descriptor 0-based in fd, the
// file_index target one-based, ALLOC raw.
func TestURingEncoders(t *testing.T) {
	type field struct {
		off  int
		size int
		want uint64
	}
	encode := func(s *sqe) []byte {
		return unsafe.Slice((*byte)(unsafe.Pointer(s)), unsafe.Sizeof(*s))
	}
	read := func(b []byte, f field) uint64 {
		switch f.size {
		case 1:
			return uint64(b[f.off])
		case 2:
			return uint64(binary.LittleEndian.Uint16(b[f.off:]))
		case 4:
			return uint64(binary.LittleEndian.Uint32(b[f.off:]))
		default:
			return binary.LittleEndian.Uint64(b[f.off:])
		}
	}
	type tc struct {
		name   string
		build  func(s *sqe)
		fields []field
	}
	payload := []byte("x")
	tests := []tc{
		{"multishot accept direct", func(s *sqe) { prepAccept(s, 7, 99, true) }, []field{
			{0, 1, uint64(opAccept)}, {2, 2, uint64(acceptMultishot)}, {4, 4, 7}, {28, 4, 0}, {32, 8, 99}, {44, 4, 0xFFFFFFFF},
		}},
		{"single-shot accept direct", func(s *sqe) { prepAccept(s, 7, 99, false) }, []field{
			{0, 1, uint64(opAccept)}, {2, 2, 0}, {4, 4, 7}, {28, 4, 0}, {44, 4, 0xFFFFFFFF},
		}},
		{"multishot recv on fixed index 0", func(s *sqe) { prepRecv(s, 0, bufGroup, 5) }, []field{
			{0, 1, uint64(opRecv)}, {1, 1, uint64(sqeFixedFile | sqeBufferSelect)}, {2, 2, uint64(recvMultishot)}, {4, 4, 0}, {40, 2, uint64(bufGroup)}, {32, 8, 5},
		}},
		{"send on fixed index 3", func(s *sqe) { prepSend(s, 3, payload, 6) }, []field{
			{0, 1, uint64(opSend)}, {1, 1, uint64(sqeFixedFile)}, {4, 4, 3}, {24, 4, 1}, {28, 4, 0x4000}, {32, 8, 6},
		}},
		{"close direct index 0", func(s *sqe) { prepCloseDirect(s, 0, 8) }, []field{
			{0, 1, uint64(opClose)}, {4, 4, 0}, {44, 4, 1}, {32, 8, 8},
		}},
		{"close direct index 41", func(s *sqe) { prepCloseDirect(s, 41, 8) }, []field{
			{44, 4, 42},
		}},
		{"cancel fixed index 2, all", func(s *sqe) { prepCancelFixed(s, 2, 9) }, []field{
			{0, 1, uint64(opAsyncCancel)}, {4, 4, 2}, {28, 4, uint64(cancelFD | cancelFDFixed | cancelAll)}, {32, 8, 9},
		}},
		{"shutdown RDWR on fixed index 4", func(s *sqe) { prepShutdownFixed(s, 4, syscall.SHUT_RDWR, 11) }, []field{
			{0, 1, uint64(opShutdown)}, {1, 1, uint64(sqeFixedFile)}, {4, 4, 4}, {8, 8, 0}, {16, 8, 0}, {24, 4, uint64(syscall.SHUT_RDWR)}, {28, 4, 0}, {32, 8, 11},
		}},
		{"shutdown on process descriptor 4", func(s *sqe) { prepShutdownFixed(s, 4, syscall.SHUT_RDWR, 11); onProcessFD(s) }, []field{
			{0, 1, uint64(opShutdown)}, {1, 1, 0}, {4, 4, 4}, {24, 4, uint64(syscall.SHUT_RDWR)},
		}},
	}
	for _, c := range tests {
		var s sqe
		c.build(&s)
		b := encode(&s)
		for _, f := range c.fields {
			if got := read(b, f); got != f.want {
				t.Errorf("%s: bytes %d..%d = %#x, want %#x", c.name, f.off, f.off+f.size, got, f.want)
			}
		}
	}
}

// TestURingUserData pins the user_data layout: the op in the top byte, 24
// bits of generation, the slot in the low 32 bits.
func TestURingUserData(t *testing.T) {
	ud := udPack(udSend, 0x1234567, 0xFFFFFFF0)
	if udOp(ud) != udSend || udGen(ud) != 0x234567 || udSlot(ud) != 0xFFFFFFF0 {
		t.Fatalf("op %d gen %#x slot %#x", udOp(ud), udGen(ud), udSlot(ud))
	}
}
