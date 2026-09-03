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
func (s *Server) adoptedSockets(name string) (files []*os.File, err error) {
	//: WithNames wraps EVERY inherited descriptor in a fresh *os.File, so
	//: calling it once per adopted name wraps each descriptor again and again
	//: and drops all but one wrapper unreferenced — and os.NewFile arms a
	//: finaliser that CLOSES the descriptor, so a dropped wrapper can close a
	//: socket another group is still about to adopt. Reading the table once and
	//: holding it on the Server keeps every wrapper reachable for the server's
	//: lifetime, which is what makes that impossible.
	s.inheritOnce.Do(func() {
		s.inherited, s.inheritErr = sdlisten.WithNames(false)
	})
	//: no inherited descriptors at all, or a descriptor we cannot interpret.
	if s.inheritErr != nil {
		//: no inherited descriptors, or one we cannot interpret.
		return nil, errs.Wrap(corenet.SocketAdoptFailed, errs.WrapParams{},
			errs.String("socket", name), errs.String("cause", s.inheritErr.Error()))
	}
	found, ok := s.inherited[name]
	//: a named socket the supervisor did not pass is a configuration mismatch
	//: between the unit file and the program, not something to bind around.
	if !ok || len(found) == 0 {
		//: refuse rather than bind our own and lose socket continuity.
		return nil, errs.Wrap(corenet.SocketAdoptFailed, errs.WrapParams{},
			errs.String("socket", name),
			errs.String("why", "no inherited socket published under that name"))
	}
	//: claimed exactly once, so two groups naming the same socket surfaces as a
	//: declaration mistake rather than serving one descriptor twice.
	delete(s.inherited, name)
	//: the descriptors the supervisor published under this name.
	return found, nil
}

// closeFiles releases descriptors an adoption did not turn into a socket.
//
// A partial adoption used to abandon them — the failing descriptor, every one
// after it, and every socket already built from the ones before. Nothing else
// reclaims those: the listeners never reached the Server's registry, so the
// Close on Start's error path had nothing to close.
func closeFiles(files []*os.File) {
	//: best-effort; the adoption error is what the caller reports.
	for _, file := range files {
		swallowErr(file.Close())
	}
}

// adoptStream turns inherited descriptors into stream listeners.
func (s *Server) adoptStream(name string) (listeners []stdnet.Listener, err error) {
	files, ferr := s.adoptedSockets(name)
	//: nothing to adopt — the error already names the socket.
	if ferr != nil {
		//: the error already names the socket.
		return nil, ferr
	}
	//: the descriptor-consuming half is separate so it can be tested with
	//: crafted descriptors: socket activation reads from fd 3, which a test
	//: binary already holds, so the real path can only be exercised across an
	//: exec.
	return listenersFromFiles(name, files)
}

// listenersFromFiles turns adopted descriptors into stream listeners, releasing
// every one of them if any fails.
func listenersFromFiles(name string, files []*os.File) (listeners []stdnet.Listener, err error) {
	out := make([]stdnet.Listener, 0, len(files))
	//: one listener per descriptor published under the name.
	for i, file := range files {
		ln, lerr := stdnet.FileListener(file)
		//: a datagram socket published where a stream one was expected lands
		//: here, which is why the error names the socket rather than the fd.
		if lerr != nil {
			//: release what this call took: the listeners already built, the
			//: descriptor that just failed, and every one still untouched.
			closeListeners(out)
			closeFiles(files[i:])
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

// closeListeners releases stream listeners built before an adoption failed.
func closeListeners(listeners []stdnet.Listener) {
	//: best-effort; the adoption error is what the caller reports.
	for _, ln := range listeners {
		swallowErr(ln.Close())
	}
}

// closePacketConns releases datagram sockets built before an adoption failed.
func closePacketConns(conns []stdnet.PacketConn) {
	//: best-effort; the adoption error is what the caller reports.
	for _, pc := range conns {
		swallowErr(pc.Close())
	}
}

// adoptPacket turns inherited descriptors into datagram sockets.
//
// sdlisten.Listeners only wraps stream sockets, so the datagram side goes
// through the raw descriptors — the consequence this domain recorded when ADR
// 0029 was written, and the reason adoption is implemented here rather than by
// widening another domain's surface.
func (s *Server) adoptPacket(name string) (conns []stdnet.PacketConn, err error) {
	files, ferr := s.adoptedSockets(name)
	//: nothing to adopt — the error already names the socket.
	if ferr != nil {
		//: the error already names the socket.
		return nil, ferr
	}
	//: separated for the same reason as the stream half.
	return packetConnsFromFiles(name, files)
}

// packetConnsFromFiles turns adopted descriptors into datagram sockets,
// releasing every one of them if any fails.
func packetConnsFromFiles(name string, files []*os.File) (conns []stdnet.PacketConn, err error) {
	out := make([]stdnet.PacketConn, 0, len(files))
	//: one socket per descriptor published under the name.
	for i, file := range files {
		pc, perr := stdnet.FilePacketConn(file)
		//: a stream socket published where a datagram one was expected lands here.
		if perr != nil {
			//: release what this call took: the sockets already built, the
			//: descriptor that just failed, and every one still untouched.
			closePacketConns(out)
			closeFiles(files[i:])
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
