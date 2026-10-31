//go:build linux && !(amd64 || arm64 || riscv64 || loong64)

// Package loop — outside io_uring's wave 1 a stop issues no SQE to count.
package loop

// stopGuardOf counts nothing: epoll serves, and its close is close(2).
func stopGuardOf(_ backendCase) stopGuard {
	//: the time bound alone.
	return nil
}
