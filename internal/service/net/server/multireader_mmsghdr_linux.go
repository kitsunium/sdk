//go:build linux

// Package server — the recvmmsg message header layout.
package server

import (
	"syscall"
	"unsafe"
)

// mmsghdrAlign is the alignment C gives struct mmsghdr, which it inherits from
// struct msghdr — the widest member decides, and msghdr holds the pointers.
const mmsghdrAlign uintptr = unsafe.Alignof(syscall.Msghdr{})

// mmsghdrStride is the distance the kernel advances between array elements:
// struct msghdr, then the uint32 length, rounded up to mmsghdrAlign.
const mmsghdrStride uintptr = (unsafe.Sizeof(syscall.Msghdr{}) +
	unsafe.Sizeof(uint32(0)) + mmsghdrAlign - 1) / mmsghdrAlign * mmsghdrAlign

// The Go layout must equal the kernel stride, and the length must sit
// immediately after the header. Both are asserted as constants, so a mismatch
// is a compile error — "constant -4 of type uintptr overflows uintptr" — on
// every architecture the tree is cross-built for. A runtime test could only
// check the word size it happens to run on, and the suite never runs on a
// 32-bit one, which is exactly where this layout was wrong.
const (
	_ uintptr = unsafe.Sizeof(mmsghdr{}) - mmsghdrStride
	_ uintptr = mmsghdrStride - unsafe.Sizeof(mmsghdr{})
	_ uintptr = unsafe.Offsetof(mmsghdr{}.length) - unsafe.Sizeof(syscall.Msghdr{})
	_ uintptr = unsafe.Sizeof(syscall.Msghdr{}) - unsafe.Offsetof(mmsghdr{}.length)
)

// mmsghdr mirrors struct mmsghdr from <sys/socket.h>:
//
//	struct mmsghdr {
//	    struct msghdr msg_hdr;
//	    unsigned int  msg_len;
//	};
//
// golang.org/x/net would supply this type, but it transitively imports
// golang.org/x/sys, banned SDK-wide (ADR 0016 / ADR 0018), so the layout is
// declared and cited here — the same discipline the proc domain already uses
// for its ABI constants.
//
// There is deliberately no explicit padding field. C rounds the struct up to
// the alignment of struct msghdr, which pads the trailing uint32 out on 64-bit
// and leaves it flush on 32-bit, where the alignment is only as wide as the
// field itself. Go applies that same trailing-padding rule to this declaration,
// so writing the padding out by hand is right on one class of architecture and
// four bytes too long on the other — and recvmmsg strides the array by the
// KERNEL's stride, which would land every element past the first at the wrong
// offset, inside a struct whose pointers the kernel also reads.
type mmsghdr struct {
	hdr    syscall.Msghdr
	length uint32
}
