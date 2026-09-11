// Package server — the listener that hands out close-tracking sockets.
package server

import (
	stdnet "net"
)

// trackedListener wraps every socket it accepts in a trackedConn.
//
// It is the only place the wrapping can happen for a TLS group: by the time
// the accept loop sees a connection, tls.NewListener has already wrapped it in
// a *tls.Conn, so the tracker has to be handed out BELOW that layer — by the
// listener TLS is built on. See trackedConn for why a group needs it at all.
type trackedListener struct {
	stdnet.Listener
}

// Accept implements net.Listener.
func (l *trackedListener) Accept() (conn stdnet.Conn, err error) {
	socket, aerr := l.Listener.Accept()
	//: verbatim, so the accept loop still recognises net.ErrClosed as the
	//: signal that its listener was closed.
	if aerr != nil {
		//: nothing was accepted.
		return nil, aerr
	}
	//: every connection this group serves can now report its own Close.
	return &trackedConn{Conn: socket}, nil
}
