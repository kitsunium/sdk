//go:build linux

package server

import (
	stdnet "net"
	"os"
	"testing"
)

// openDescriptors counts this process's open file descriptors.
//
// It is the only assertion that actually measures a leak. Checking that Close
// was called proves the code took a path, not that the kernel got the
// descriptor back — and a descriptor leak is precisely a divergence between
// those two.
func openDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	//: the ReadDir handle itself is one of the entries and is already closed
	//: by the time this returns, so the count is only ever compared with
	//: another taken the same way.
	return len(entries)
}

// adoptableListenerFile returns a descriptor a stream adoption accepts.
func adoptableListenerFile(t *testing.T) *os.File {
	t.Helper()
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() {
		//: File dups the descriptor, so this listener is no longer needed.
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("close listener: %v", cerr)
		}
	}()
	file, ferr := ln.(*stdnet.TCPListener).File()
	if ferr != nil {
		t.Fatalf("listener file: %v", ferr)
	}
	return file
}

// unadoptableFile returns a descriptor no socket adoption can accept.
func unadoptableFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "not-a-socket")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	return file
}

// TestPartialStreamAdoptionReleasesEveryDescriptor pins that a failed adoption
// gives every descriptor back.
//
// The loop returned on the first descriptor it could not wrap, abandoning the
// listeners already built from the ones before it, the descriptor that just
// failed, and every one after. Nothing else could reclaim them: those listeners
// never reached the Server's registry, so the Close on Start's error path had
// nothing to close, and a supervisor restarting a service into this path leaks
// a descriptor per attempt.
func TestPartialStreamAdoptionReleasesEveryDescriptor(t *testing.T) {
	//: not parallel — it counts process-wide descriptors.
	good := adoptableListenerFile(t)
	bad := unadoptableFile(t)
	files := []*os.File{good, bad}

	before := openDescriptors(t)
	listeners, err := listenersFromFiles("api", files)
	//: the second descriptor is not a listening socket, so adoption must fail.
	if err == nil {
		closeListeners(listeners)
		t.Fatal("a regular file was adopted as a stream listener")
	}
	after := openDescriptors(t)
	//: both inherited descriptors were consumed by the attempt, and the
	//: listener built from the first must have gone with them.
	if want := before - len(files); after != want {
		t.Fatalf("open descriptors = %d after a failed adoption, want %d — "+
			"the attempt kept %d descriptor(s) nothing can reclaim",
			after, want, after-want)
	}
}

// TestPartialPacketAdoptionReleasesEveryDescriptor is the same property on the
// datagram half, which had the byte-identical defect.
func TestPartialPacketAdoptionReleasesEveryDescriptor(t *testing.T) {
	//: not parallel — it counts process-wide descriptors.
	good := adoptablePacketFile(t)
	bad := unadoptableFile(t)
	files := []*os.File{good, bad}

	before := openDescriptors(t)
	conns, err := packetConnsFromFiles("dns", files)
	//: the second descriptor is not a datagram socket, so adoption must fail.
	if err == nil {
		closePacketConns(conns)
		t.Fatal("a regular file was adopted as a datagram socket")
	}
	after := openDescriptors(t)
	//: both inherited descriptors were consumed by the attempt, and the socket
	//: built from the first must have gone with them.
	if want := before - len(files); after != want {
		t.Fatalf("open descriptors = %d after a failed adoption, want %d — "+
			"the attempt kept %d descriptor(s) nothing can reclaim",
			after, want, after-want)
	}
}

// adoptablePacketFile returns a descriptor a datagram adoption accepts.
func adoptablePacketFile(t *testing.T) *os.File {
	t.Helper()
	pc, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen packet: %v", err)
	}
	defer func() {
		//: File dups the descriptor, so this socket is no longer needed.
		if cerr := pc.Close(); cerr != nil {
			t.Errorf("close socket: %v", cerr)
		}
	}()
	file, ferr := pc.(*stdnet.UDPConn).File()
	if ferr != nil {
		t.Fatalf("socket file: %v", ferr)
	}
	return file
}
