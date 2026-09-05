//go:build linux

// Package sdnotify_test — the Linux round trip. The listener verifies the
// sender's credentials through SO_PASSCRED, a Linux facility, so the loop that
// closes notifier and listener over a real socket is gated on the same tag as
// the service implementation rather than guessed at runtime.
package sdnotify_test

import (
	"os"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/sdnotify"
)

// TestRoundTripThroughFacade drives Listen/Notify/Recv over a real socket. The
// facade is a thin delegation, so what is worth pinning here is that the three
// halves agree on one socket path and one credential check — a mismatch would
// only ever show up as silence at runtime, which is exactly the failure systemd
// readiness exists to prevent.
func TestRoundTripThroughFacade(t *testing.T) {
	type tc struct {
		name  string
		send  func() error
		ready bool
	}
	tests := []tc{
		{"Ready", sdnotify.Ready, true},
		{"Notify carrying READY", func() error {
			return sdnotify.Notify(map[string]string{"READY": "1"})
		}, true},
		{"Notify carrying a status only", func() error {
			return sdnotify.Notify(map[string]string{"STATUS": "warming up"})
		}, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		l, path, err := sdnotify.Listen()
		if err != nil {
			t.Fatalf("Listen() = %v, want nil", err)
		}
		defer func() {
			if cerr := l.Close(); cerr != nil {
				t.Errorf("closing the listener: %v", cerr)
			}
		}()

		//: point the notifier at the listener we just stood up.
		t.Setenv("NOTIFY_SOCKET", path)
		if serr := c.send(); serr != nil {
			t.Fatalf("%s = %v, want nil", c.name, serr)
		}

		n, err := l.Recv()
		if err != nil {
			t.Fatalf("Recv() after %s = %v, want nil", c.name, err)
		}
		if n.Ready() != c.ready {
			t.Errorf("Recv().Ready() after %s = %v, want %v", c.name, n.Ready(), c.ready)
		}
		//: the credential check is the whole point of the Linux listener: a
		//: datagram attributed to another pid would let any local process
		//: declare our service ready.
		if n.SenderPID != os.Getpid() {
			t.Errorf("Recv().SenderPID = %d, want %d", n.SenderPID, os.Getpid())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
