//go:build !windows

package server

import (
	"errors"
	stdnet "net"
	"syscall"
)

// rebindAttempts is one: an address in use elsewhere is a real conflict, and
// a TIME_WAIT connection does not hold a listener's address (Go binds with
// SO_REUSEADDR), so nothing is worth waiting out.
const rebindAttempts int = 1

// exclusiveAddrSupported reports that the platform has no SO_EXCLUSIVEADDRUSE.
func exclusiveAddrSupported() bool {
	//: Windows' option alone.
	return false
}

// setExclusiveAddr does nothing where the option does not exist.
func setExclusiveAddr(_ uintptr) error {
	//: never called: exclusiveAddrSupported said no.
	return nil
}

// exclusiveAddrOf reads nothing where the option does not exist.
func exclusiveAddrOf(_ uintptr) (bool, error) {
	//: no such option.
	return false, nil
}

// bindConflict reports a bind refused because another socket holds the
// address: EADDRINUSE (#1330).
func bindConflict(err error) bool {
	//: the errno the net package wraps.
	return errors.Is(err, syscall.EADDRINUSE)
}

// addrInUse never asks for a rebind: rebindAttempts is one.
func addrInUse(_ error) bool {
	//: not waited out.
	return false
}

// closeConn closes c; hard changes nothing where no close is graceful.
func closeConn(c stdnet.Conn, _ bool) error {
	//: the platform's close.
	return c.Close()
}
