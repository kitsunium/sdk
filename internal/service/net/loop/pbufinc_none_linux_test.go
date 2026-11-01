//go:build linux && !(amd64 || arm64 || riscv64 || loong64)

// Package loop — outside io_uring's wave 1 no ring consumes buffers at all.
package loop

// consumesIncrementally is false: epoll serves.
func consumesIncrementally(_ *Group) bool {
	//: no ring.
	return false
}
