//go:build unix && !(linux && (amd64 || arm64 || riscv64 || loong64))

package loop

import "syscall"

// closePacketFD closes the descriptor a refused lane was handed; a negative
// one is none.
func closePacketFD(fd int) {
	//: something was handed over: the lane owned it from the call on.
	if fd >= 0 {
		swallowErr(syscall.Close(fd))
	}
}
