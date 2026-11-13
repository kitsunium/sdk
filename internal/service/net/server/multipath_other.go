//go:build !linux

package server

import "syscall"

// multipathOf is Linux's: Go opens Multipath TCP by default on Linux alone.
func multipathOf(syscall.Conn) bool {
	//: plain.
	return false
}
