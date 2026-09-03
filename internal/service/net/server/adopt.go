// Package server — adoption of listeners inherited from a supervisor.
package server

import (
	stdnet "net"
	"os"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/sdlisten"
)

// adoptedSockets recovers the inherited sockets published under one name.
//
// Socket activation is what makes a zero-downtime restart possible: the
// supervisor keeps the bound socket across the exec, so no connection is lost
// and no bind races. The engine therefore adopts rather than binds when a group
// names an inherited socket.
//
// unsetEnv is false: the group may name several sockets, and clearing the
// environment on the first lookup would make every later one come back empty.
// The caller clears it once, after startup, if it spawns children of its own.
func adoptedSockets(name string) (files []*os.File, err error) {
	named, lerr := sdlisten.WithNames(false)
	//: no inherited descriptors at all, or a descriptor we cannot interpret.
	if lerr != nil {
		//: no inherited descriptors, or one we cannot interpret.
		return nil, errs.Wrap(corenet.SocketAdoptFailed, errs.WrapParams{},
			errs.String("socket", name), errs.String("cause", lerr.Error()))
	}
	found, ok := named[name]
	//: a named socket the supervisor did not pass is a configuration mismatch
	//: between the unit file and the program, not something to bind around.
	if !ok || len(found) == 0 {
		//: refuse rather than bind our own and lose socket continuity.
		return nil, errs.Wrap(corenet.SocketAdoptFailed, errs.WrapParams{},
			errs.String("socket", name),
			errs.String("why", "no inherited socket published under that name"))
	}
	//: the descriptors the supervisor published under this name.
	return found, nil
}

// adoptStream turns inherited descriptors into stream listeners.
func adoptStream(name string) (listeners []stdnet.Listener, err error) {
	files, ferr := adoptedSockets(name)
	//: nothing to adopt — the error already names the socket.
	if ferr != nil {
		//: the error already names the socket.
		return nil, ferr
	}
	out := make([]stdnet.Listener, 0, len(files))
	//: one listener per descriptor published under the name.
	for _, file := range files {
		ln, lerr := stdnet.FileListener(file)
		//: a datagram socket published where a stream one was expected lands
		//: here, which is why the error names the socket rather than the fd.
		if lerr != nil {
			//: name the socket, not the descriptor number.
			return nil, errs.Wrap(corenet.SocketAdoptFailed, errs.WrapParams{},
				errs.String("socket", name), errs.String("cause", lerr.Error()))
		}
		//: FileListener dups the descriptor, so the original is ours to close.
		swallowErr(file.Close())
		out = append(out, ln)
	}
	//: every descriptor became a live listener.
	return out, nil
}

// adoptPacket turns inherited descriptors into datagram sockets.
//
// sdlisten.Listeners only wraps stream sockets, so the datagram side goes
// through the raw descriptors — the consequence this domain recorded when ADR
// 0029 was written, and the reason adoption is implemented here rather than by
// widening another domain's surface.
func adoptPacket(name string) (conns []stdnet.PacketConn, err error) {
	files, ferr := adoptedSockets(name)
	//: nothing to adopt — the error already names the socket.
	if ferr != nil {
		//: the error already names the socket.
		return nil, ferr
	}
	out := make([]stdnet.PacketConn, 0, len(files))
	//: one socket per descriptor published under the name.
	for _, file := range files {
		pc, perr := stdnet.FilePacketConn(file)
		//: a stream socket published where a datagram one was expected lands here.
		if perr != nil {
			//: name the socket, not the descriptor number.
			return nil, errs.Wrap(corenet.SocketAdoptFailed, errs.WrapParams{},
				errs.String("socket", name), errs.String("cause", perr.Error()))
		}
		//: FilePacketConn dups the descriptor, so the original is ours to close.
		swallowErr(file.Close())
		out = append(out, pc)
	}
	//: every descriptor became a live datagram socket.
	return out, nil
}
