//go:build linux

// Package server — the width-correct kernel length assignment.
package server

// setKernelLen assigns n to a kernel length field.
//
// The length fields of syscall.Iovec and syscall.Msghdr are sized after the
// C `size_t`, so their Go type follows the word size: uint64 on 64-bit Linux
// (amd64, arm64, riscv64, …) and uint32 on 32-bit (386, arm, mips, …). A
// literal conversion therefore compiles on one class of architecture and fails
// on the other — the cross-build for linux/386 and linux/arm caught exactly
// that.
//
// The type parameter is inferred from the field being written, so one call
// site is correct on every architecture without a build tag per word size and
// without golang.org/x/sys, which ADR 0016 and ADR 0018 ban.
//
// The caller passes an int that the kernel will treat as a size. Values here
// are buffer lengths and small counts, both far below the 32-bit ceiling, so
// the conversion cannot truncate in practice; the ceiling is asserted in the
// tests rather than left to trust.
func setKernelLen[T ~uint32 | ~uint64](dst *T, n int) {
	*dst = T(n)
}
