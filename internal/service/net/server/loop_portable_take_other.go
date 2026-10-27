//go:build !unix

package server

import stdnet "net"

// ownListeners keeps server's listeners where a listener's descriptor cannot
// be duplicated into one of the portable backend's own (Windows): server
// closes them after the grace, and the drain takes no queue.
func ownListeners(lns []stdnet.Listener) (all, owned []stdnet.Listener) {
	//: server's, every one.
	return lns, nil
}

// takeQueue takes nothing where no listener is the group's own.
func takeQueue(_ stdnet.Listener, _ func(stdnet.Conn)) {}
