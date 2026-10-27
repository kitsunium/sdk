//go:build unix

package server

import (
	stdnet "net"
	"os"
	"syscall"
)

// portableTakeMax bounds how many connections the portable drain takes from
// one listener's queue: past it the arrivals outpace the drain.
const portableTakeMax int = 1 << 16

// filer is a listener whose descriptor can be duplicated.
type filer interface {
	// File returns a duplicate of the listener's descriptor.
	File() (*os.File, error)
}

// ownListeners duplicates each listener server bound, so the portable
// backend closes its own copy once its drain took the queue, and server's
// close after the grace leaves the socket listening until then. A listener
// that cannot be duplicated stays server's, closed with the others.
func ownListeners(lns []stdnet.Listener) (all, owned []stdnet.Listener) {
	//: each one, duplicated when it can be.
	for _, ln := range lns {
		dup, err := dupListener(ln)
		//: server's, closed by server.
		if err != nil {
			all = append(all, ln)
			continue
		}
		all = append(all, dup)
		owned = append(owned, dup)
	}
	//: the acceptors' listeners, and the ones the group closes.
	return all, owned
}

// dupListener is a listener of its own over a duplicate of ln's descriptor.
func dupListener(ln stdnet.Listener) (stdnet.Listener, error) {
	f, ok := ln.(filer)
	//: nothing to duplicate.
	if !ok {
		//: refused.
		return nil, syscall.EINVAL
	}
	file, err := f.File()
	//: no duplicate.
	if err != nil {
		//: refused.
		return nil, err
	}
	//: FileListener duplicates it again; this copy closes either way.
	defer func() { swallowErr(file.Close()) }()
	//: its own listener.
	return stdnet.FileListener(file)
}

// takeQueue accepts from ln, non-blocking, until its queue is empty, and
// hands each connection to admit: the acceptor may take some too, and both
// admit.
func takeQueue(ln stdnet.Listener, admit func(stdnet.Conn)) {
	sc, ok := ln.(syscall.Conn)
	//: no descriptor to accept on.
	if !ok {
		//: nothing taken.
		return
	}
	rc, err := sc.SyscallConn()
	//: no raw access.
	if err != nil {
		//: nothing taken.
		return
	}
	//: bounded: past it the arrivals outpace the drain.
	for range portableTakeMax {
		nfd, aerr := acceptRaw(rc)
		//: one the peer abandoned, or a signal: the next one.
		if aerr == syscall.ECONNABORTED || aerr == syscall.EINTR {
			continue
		}
		//: empty (EAGAIN), out of descriptors, or the listener failed.
		if aerr != nil {
			//: taken.
			return
		}
		c, cerr := connOf(nfd)
		//: a descriptor the runtime would not take: closed by connOf.
		if cerr != nil {
			continue
		}
		admit(c)
	}
}

// acceptRaw accepts once on rc's non-blocking descriptor.
func acceptRaw(rc syscall.RawConn) (int, error) {
	nfd := -1
	var aerr error
	cerr := rc.Control(func(fd uintptr) { nfd, _, aerr = syscall.Accept(int(fd)) })
	//: the listener closed under the take.
	if cerr != nil {
		//: over.
		return -1, cerr
	}
	//: the accepted descriptor, or the kernel's answer.
	return nfd, aerr
}

// connOf wraps the accepted descriptor nfd in a net.Conn on the netpoller,
// and closes nfd whatever happens: FileConn holds a duplicate.
func connOf(nfd int) (stdnet.Conn, error) {
	syscall.CloseOnExec(nfd)
	f := os.NewFile(uintptr(nfd), "")
	defer func() { swallowErr(f.Close()) }()
	//: on the netpoller.
	return stdnet.FileConn(f)
}
