//go:build !linux

package server

import "syscall"

// userTimeoutControl is Linux's: elsewhere it is not set, as the option says.
func userTimeoutControl(socketSpec, string, syscall.RawConn) error {
	//: nothing to set.
	return nil
}
