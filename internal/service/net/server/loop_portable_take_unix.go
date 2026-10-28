//go:build unix

package server

import (
	"errors"
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

// portableTake is one owned listener's take, across the rounds of the
// drain: done once the listener is closed; held the connections accepted but
// not yet wrapped for want of a descriptor, kept open for the next round.
type portableTake struct {
	done bool
	held []*os.File
}

// takeQueue accepts from ln, non-blocking, until its queue is empty, and
// hands each connection to admit: the acceptor may take some too, and both
// admit. It reports whether the take waits for descriptors: an accept, or
// the duplicate a connection's wrapping needs, out of them. A connection
// accepted but not wrapped stays in take.held, open, for the next round.
func takeQueue(ln stdnet.Listener, admit func(stdnet.Conn), take *portableTake) bool {
	//: what the last round could not wrap, first.
	if wrapHeld(take, admit) {
		//: still out of descriptors.
		return true
	}
	sc, ok := ln.(syscall.Conn)
	//: no descriptor to accept on.
	if !ok {
		//: nothing taken.
		return false
	}
	rc, err := sc.SyscallConn()
	//: no raw access.
	if err != nil {
		//: nothing taken.
		return false
	}
	//: bounded: past it the arrivals outpace the drain.
	for range portableTakeMax {
		nfd, aerr := acceptRaw(rc)
		//: by what the failure says of the queue.
		switch {
		//: one connection lost, the queue still there.
		case aerr != nil && acceptRetries(aerr):
			continue
		//: no descriptor until a close frees one.
		case aerr == syscall.EMFILE || aerr == syscall.ENFILE:
			//: next round.
			return true
		//: empty (EAGAIN), or the listener failed.
		case aerr != nil:
			//: taken.
			return false
		}
		take.held = append(take.held, os.NewFile(uintptr(nfd), ""))
		//: the wrapping is out of descriptors.
		if wrapHeld(take, admit) {
			//: next round.
			return true
		}
	}
	//: past the budget.
	return false
}

// wrapHeld wraps every held connection in a net.Conn on the netpoller and
// admits it, and reports whether one is still held for want of a descriptor.
func wrapHeld(take *portableTake, admit func(stdnet.Conn)) bool {
	//: in the order accepted.
	for len(take.held) > 0 {
		f := take.held[0]
		c, err := stdnet.FileConn(f)
		//: no descriptor for the duplicate: kept open, tried again.
		if errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) {
			//: held.
			return true
		}
		take.held = take.held[1:]
		swallowErr(f.Close())
		//: anything else the runtime refused: closed.
		if err != nil {
			continue
		}
		admit(c)
	}
	//: none held.
	return false
}

// closeRaw closes the connections a take could never wrap.
func closeRaw(held []*os.File) {
	//: each one.
	for _, f := range held {
		swallowErr(f.Close())
	}
}

// acceptRetries reports whether an accept's failure lost one connection and
// left the queue: the peer abandoned it, a signal, or one of the network
// errors accept(2) says to retry on.
func acceptRetries(err error) bool {
	//: by errno.
	switch err {
	//: retried.
	case syscall.ECONNABORTED, syscall.EINTR, syscall.EPROTO, syscall.ENETDOWN, syscall.ENOPROTOOPT,
		syscall.EHOSTDOWN, syscall.EHOSTUNREACH, syscall.EOPNOTSUPP, syscall.ENETUNREACH:
		//: the next one.
		return true
	//: anything else.
	default:
		//: not a retry.
		return false
	}
}

// acceptRaw accepts once on rc's non-blocking descriptor, close-on-exec.
func acceptRaw(rc syscall.RawConn) (int, error) {
	nfd := -1
	var aerr error
	cerr := rc.Control(func(fd uintptr) { nfd, aerr = acceptCloexec(int(fd)) })
	//: the listener closed under the take.
	if cerr != nil {
		//: over.
		return -1, cerr
	}
	//: the accepted descriptor, or the kernel's answer.
	return nfd, aerr
}
