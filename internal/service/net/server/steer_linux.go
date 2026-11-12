//go:build linux

package server

import (
	"cmp"
	stdnet "net"
	"runtime"
	"syscall"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// soAttachReuseportCBPF is SO_ATTACH_REUSEPORT_CBPF
	// (include/uapi/asm-generic/socket.h, Linux 4.5): a classic BPF program that
	// picks the socket of a SO_REUSEPORT group by its return value.
	soAttachReuseportCBPF int = 51
	// skfAdOff is SKF_AD_OFF (include/uapi/linux/filter.h): an absolute load past
	// it reads a socket-filter extension.
	skfAdOff int32 = -0x1000
	// skfAdCPU is SKF_AD_CPU, the extension of the CPU the softirq runs on.
	skfAdCPU int32 = 36
	// bpfLdWAbs is BPF_LD|BPF_W|BPF_ABS (include/uapi/linux/bpf_common.h).
	bpfLdWAbs uint16 = 0x20
	// bpfAluModK is BPF_ALU|BPF_MOD|BPF_K.
	bpfAluModK uint16 = 0x94
	// bpfRetA is BPF_RET|BPF_A.
	bpfRetA uint16 = 0x16
)

// sockFilter is struct sock_filter (include/uapi/linux/filter.h).
type sockFilter struct {
	// code, jt and jf are the opcode and its jump offsets.
	code   uint16
	jt, jf uint8
	// k is the operand.
	k uint32
}

// sockFprog is struct sock_fprog: the length, then the instructions.
type sockFprog struct {
	// length is the program's instruction count.
	length uint16
	// filter is the program.
	filter *sockFilter
}

// attachSteer installs the program that hands each datagram to the socket of
// index cpu mod n of the group's sockets, on the first of them: the kernel keeps
// one program per reuseport group, whichever socket carries it. EINVAL or EPERM,
// a seccomp filter or a kernel before 4.5, leave the hash spread (#977).
func attachSteer(sockets []stdnet.PacketConn) error {
	//: one socket spreads over nothing, and a program modulo one answers zero.
	if len(sockets) <= 1 {
		//: nothing to attach.
		return nil
	}
	program := cpuModProgram(uint32(len(sockets)))
	raw, err := packetSocket(sockets[0]).SyscallConn()
	//: without the descriptor there is nothing to attach to.
	if err != nil {
		//: the row's reason.
		return err
	}
	var attachErr error
	cerr := raw.Control(func(fd uintptr) {
		attachErr = steerAttach(int(fd), program)
	})
	//: the socket's failure first, then the kernel's answer, nil once attached.
	return cmp.Or(cerr, attachErr)
}

// steerAttach is the setsockopt attachSteer makes; a test swaps it to answer what
// a seccomp filter would.
var steerAttach func(fd int, program []sockFilter) error = func(fd int, program []sockFilter) error {
	//: the kernel reads the program through the pointer inside sock_fprog, so
	//: the instructions must stay where they are for the call.
	var pin runtime.Pinner
	pin.Pin(&program[0])
	defer pin.Unpin()
	prog := sockFprog{length: uint16(len(program)), filter: &program[0]}
	//: SetsockoptString is what every linux arch's setsockopt goes through
	//: (socketcall on 386), and copies the bytes of sock_fprog; its pointer
	//: inside them is the one pinned above.
	bytesOf := unsafe.String((*byte)(unsafe.Pointer(&prog)), unsafe.Sizeof(prog))
	if err := syscall.SetsockoptString(fd, syscall.SOL_SOCKET, soAttachReuseportCBPF, bytesOf); err != nil {
		//: the kernel's refusal, named on the row.
		return errs.Wrap(corenet.ListenFailed, errs.WrapParams{}, errs.String("cause", "setsockopt SO_ATTACH_REUSEPORT_CBPF: "+err.Error()))
	}
	//: attached.
	return nil
}

// cpuModProgram is the steering program: A = cpu; A %= n; return A.
func cpuModProgram(n uint32) []sockFilter {
	offset := skfAdOff + skfAdCPU
	//: three instructions.
	return []sockFilter{
		{code: bpfLdWAbs, k: uint32(offset)},
		{code: bpfAluModK, k: n},
		{code: bpfRetA},
	}
}
