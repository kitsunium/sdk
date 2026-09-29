package kit

import (
	"context"
	"io"
	"strings"
	"testing"
)

// kit's listener is one socket on one address: a second product on a busy
// port is refused, the way it is on every platform, and ":0" is one port —
// the one App.URL says. The SDK's engine could shard an address with
// SO_REUSEPORT; kit asks it not to.
func TestTheListenerIsOneSocket(t *testing.T) {
	svc := NewService("listens", "A service with nothing to declare.")
	first := NewApp("first", svc).With(InMemory(), Listen("127.0.0.1:0"), Logs(io.Discard))
	if err := first.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer first.Stop(context.Background())
	first.mu.Lock()
	srv, addr := first.server, first.addr
	first.mu.Unlock()
	if n := len(srv.State().Listeners); n != 1 {
		t.Errorf("%d listening sockets on %s, want 1", n, addr)
	}

	other := NewService("listens-too", "Another service with nothing to declare.")
	second := NewApp("second", other).With(InMemory(), Listen(addr), Logs(io.Discard))
	err := second.Start(t.Context())
	if err == nil {
		second.Stop(context.Background())
	}
	if err == nil || !strings.Contains(err.Error(), "could not listen on its address") {
		t.Fatalf("a second product on %s: %v", addr, err)
	}
}
