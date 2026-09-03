//go:build linux

// Package server — the recvmmsg message header layout.
package server

import "syscall"

// mmsghdrPadding aligns mmsghdr to 8 bytes on 64-bit, which the kernel assumes
// when it strides the array. Declaring it rather than writing 4 inline keeps the
// reason next to the number.
const mmsghdrPadding int = 4

// mmsghdr mirrors struct mmsghdr from <sys/socket.h>:
//
//	struct mmsghdr {
//	    struct msghdr msg_hdr;
//	    unsigned int  msg_len;
//	};
//
// The trailing padding aligns the struct to 8 bytes on 64-bit, which the kernel
// assumes when it strides the array. golang.org/x/net would supply this type,
// but it transitively imports golang.org/x/sys, banned SDK-wide (ADR 0016 /
// ADR 0018), so the layout is declared and cited here — the same discipline the
// proc domain already uses for its ABI constants.
type mmsghdr struct {
	hdr    syscall.Msghdr
	length uint32
	_      [mmsghdrPadding]byte
}
