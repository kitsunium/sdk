//go:build linux

// Package server — the descriptor-bearing socket contract.
package server

import "syscall"

// rawConnProvider is the part of *net.UDPConn the batched reader needs: access
// to the raw descriptor under the runtime's netpoller.
//
// Declaring the contract rather than asserting the concrete *net.UDPConn keeps
// a wrapper or a test double able to opt in, and keeps the fallback honest for
// anything that cannot.
type rawConnProvider interface {
	SyscallConn() (syscall.RawConn, error)
}
