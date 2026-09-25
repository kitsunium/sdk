//go:build unix

// Package server — a descriptor a supervisor could pass, on the kernels where a
// socket can become one.
package server

import (
	stdnet "net"
	"os"
	"testing"
)

// listenerFile returns the descriptor of a bound TCP listener, as a supervisor
// passes one to the process it starts.
func listenerFile(t *testing.T) *os.File {
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
	tcp, ok := ln.(*stdnet.TCPListener)
	if !ok {
		t.Fatalf("Listen returned a %T, want *net.TCPListener", ln)
	}
	dup, ferr := tcp.File()
	if ferr != nil {
		t.Fatalf("listener file: %v", ferr)
	}
	return dup
}

// packetFile is listenerFile for a bound UDP socket.
func packetFile(t *testing.T) *os.File {
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
	udp, ok := pc.(*stdnet.UDPConn)
	if !ok {
		t.Fatalf("ListenPacket returned a %T, want *net.UDPConn", pc)
	}
	dup, ferr := udp.File()
	if ferr != nil {
		t.Fatalf("socket file: %v", ferr)
	}
	return dup
}
