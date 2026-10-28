// Package server — the portable lane's last events: a connection handed over
// as the lane stopped is closed and uncounted.
package server

import (
	stdnet "net"
	"testing"
	"time"
)

// TestPortable_openAfterStopUncounted pins that a pevOpen the drainer takes,
// after the lane stopped, leaves live as it found it: admit counted the
// connection, and neither open nor finish ever ran for it.
func TestPortable_openAfterStopUncounted(t *testing.T) {
	pl := newOpenPortable(&portableLineH{}, 0)
	pl.closeAll()
	server, client := stdnet.Pipe()
	defer func() { swallowErr(client.Close()) }()
	//: admit's count, then its send, as the lane already stopped.
	pl.live.Add(1)
	pl.events <- portableEvent{kind: pevOpen, conn: server}
	deadline := time.Now().Add(5 * time.Second)
	for pl.live.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("live = %d after the drainer took the pevOpen, want 0", pl.live.Load())
		}
		time.Sleep(time.Millisecond)
	}
	//: the drainer closed the connection, never left it open: the peer's
	//: read ends, a pipe having no deadline once its other end closed.
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("read on the handed-over connection succeeded: it was left open")
	}
}
