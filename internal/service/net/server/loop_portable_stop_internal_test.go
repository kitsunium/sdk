// Package server — what a stopped portable lane does with the events it
// will never handle: a connection handed over and never opened is closed and
// uncounted, any other event dropped.
package server

import (
	stdnet "net"
	"testing"
	"time"
)

// Test_portableDiscard pins that the events buffered for a stopped lane are
// emptied without waiting, each connection handed over closed — a
// handshake that won its send after the lane stopped leaks nothing — and the
// lane's live count released, while a data event closes nothing.
func Test_portableDiscard(t *testing.T) {
	t.Parallel()
	pl := &portableLoop{events: make(chan portableEvent, 4)}
	opened, peer := stdnet.Pipe()
	t.Cleanup(func() { swallowErr(peer.Close()) })
	other, otherPeer := stdnet.Pipe()
	t.Cleanup(func() { swallowErr(other.Close()); swallowErr(otherPeer.Close()) })
	pl.live.Add(1)
	pl.events <- portableEvent{kind: pevOpen, conn: opened}
	pl.events <- portableEvent{kind: pevData, conn: other, data: []byte("x")}
	pl.discardBuffered()
	if n := len(pl.events); n != 0 {
		t.Fatalf("%d events left buffered", n)
	}
	if n := pl.live.Load(); n != 0 {
		t.Fatalf("live = %d after a discarded open, want 0", n)
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("the discarded open's connection is still open")
	}
	//: a data event's connection belongs to closeAll, not to the drainer.
	go func() { _, _ = otherPeer.Write([]byte("y")) }()
	_ = other.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := other.Read(make([]byte, 1)); err != nil {
		t.Fatalf("a data event's connection was closed: %v", err)
	}
}
