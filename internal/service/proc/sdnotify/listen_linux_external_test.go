//go:build linux

// Package sdnotify_test — the listener as a supervisor uses it.
package sdnotify_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsdnotify "github.com/kitsunium/sdk/internal/service/proc/sdnotify"
)

// TestListen pins the socket a supervisor hands to its children, and the two
// properties that make it safe to hand out.
//
// It lives in a PRIVATE 0700 directory, so the path is unguessable — a notify
// socket in a shared temp directory is reachable by any local process, and
// anything that can reach it can declare the service ready. And each Listen
// creates its OWN, so two supervisors in one process do not share an endpoint.
func TestListen(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many listeners to stand up; more than one proves isolation.
		count int
	}
	tests := []tc{
		{"a single listener", 1},
		{"several listeners", 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(map[string]struct{}, c.count)
		for range c.count {
			l, socketPath, err := svcsdnotify.Listen()
			if err != nil {
				t.Fatalf("Listen = %v, want nil", err)
			}
			closeLater(t, l)

			//: the path is what a child is given as $NOTIFY_SOCKET, so it has
			//: to be a real, bound socket.
			info, serr := os.Stat(socketPath)
			if serr != nil {
				t.Fatalf("the socket is not there: %v", serr)
			}
			if info.Mode()&os.ModeSocket == 0 {
				t.Errorf("%s is not a socket: %v", socketPath, info.Mode())
			}
			//: the private directory must not be world-readable, or the
			//: unguessable path stops being unguessable.
			dirInfo, derr := os.Stat(filepath.Dir(socketPath))
			if derr != nil {
				t.Fatalf("the private directory is not there: %v", derr)
			}
			if dirInfo.Mode().Perm()&0o077 != 0 {
				t.Errorf("the private directory is mode %v, want no group or other access", dirInfo.Mode().Perm())
			}
			//: every listener gets its own endpoint.
			if _, dup := seen[socketPath]; dup {
				t.Errorf("two listeners bound the same path %s", socketPath)
			}
			seen[socketPath] = struct{}{}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestListenRoundTrip pins the whole protocol through the public surface: a
// notifier pointed at the returned path sends, and the listener receives the
// fields AND the kernel-verified sender pid.
//
// The pid is what makes the notification trustworthy. Anything on the host that
// finds the socket can write to it, so the supervisor has to know who actually
// sent a READY=1 before acting on it.
func TestListenRoundTrip(t *testing.T) {
	//: not parallel — every case points $NOTIFY_SOCKET at its own listener,
	//: and that variable is process-wide.
	type tc struct {
		name string
		//: the state the notifier sends.
		state map[string]string
		//: fields the received notification must carry.
		wantState   map[string]string
		wantStatus  string
		wantMainPID int
		wantReady   bool
	}
	tests := []tc{
		{
			name:      "a readiness notification",
			state:     map[string]string{"READY": "1"},
			wantState: map[string]string{"READY": "1"},
			wantReady: true,
		},
		{
			name:       "a status line",
			state:      map[string]string{"STATUS": "warming up"},
			wantState:  map[string]string{"STATUS": "warming up"},
			wantStatus: "warming up",
		},
		{
			//: MAINPID is lifted into its own typed field, because a
			//: supervisor acts on it rather than merely logging it.
			name:        "a main pid",
			state:       map[string]string{"MAINPID": "4242"},
			wantState:   map[string]string{"MAINPID": "4242"},
			wantMainPID: 4242,
		},
		{
			name: "several fields at once",
			state: map[string]string{
				"READY": "1", "STATUS": "serving", "MAINPID": "4242",
			},
			wantState:   map[string]string{"READY": "1", "STATUS": "serving"},
			wantStatus:  "serving",
			wantMainPID: 4242,
			wantReady:   true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		l, socketPath, err := svcsdnotify.Listen()
		if err != nil {
			t.Fatalf("Listen = %v, want nil", err)
		}
		defer func() {
			if cerr := l.Close(); cerr != nil {
				t.Logf("closing the listener: %v", cerr)
			}
		}()

		//: point the notifier at this listener and send.
		t.Setenv("NOTIFY_SOCKET", socketPath)
		if nerr := svcsdnotify.Notify(c.state); nerr != nil {
			t.Fatalf("Notify = %v, want nil", nerr)
		}

		got, rerr := l.Recv()
		if rerr != nil {
			t.Fatalf("Recv = %v, want nil", rerr)
		}

		for name, want := range c.wantState {
			if got.State[name] != want {
				t.Errorf("State[%q] = %q, want %q", name, got.State[name], want)
			}
		}
		if got.Status != c.wantStatus {
			t.Errorf("Status = %q, want %q", got.Status, c.wantStatus)
		}
		if got.MainPID != c.wantMainPID {
			t.Errorf("MainPID = %d, want %d", got.MainPID, c.wantMainPID)
		}
		if got.Ready() != c.wantReady {
			t.Errorf("Ready() = %v, want %v", got.Ready(), c.wantReady)
		}
		//: the kernel attributes the datagram to THIS process, whatever the
		//: body claimed.
		if got.SenderPID != os.Getpid() {
			t.Errorf("SenderPID = %d, want %d", got.SenderPID, os.Getpid())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestListenRejectsTruncatedDatagram pins the size guard from the outside. A
// datagram longer than the receive buffer arrives CLIPPED, and a clipped
// environment block still parses — it just quietly loses whatever fell off the
// end. Acting on half a notification is worse than refusing it.
func TestListenRejectsTruncatedDatagram(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the payload length to send, relative to the 4096-byte buffer.
		size int
	}
	tests := []tc{
		{"exactly the buffer size", 4096},
		{"one byte over", 4097},
		{"far over", 16384},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		l, socketPath, err := svcsdnotify.Listen()
		if err != nil {
			t.Fatalf("Listen = %v, want nil", err)
		}
		defer func() {
			if cerr := l.Close(); cerr != nil {
				t.Logf("closing the listener: %v", cerr)
			}
		}()

		//: write the oversized body directly, bypassing the encoder's own
		//: limits — this is what a hostile or buggy client would do.
		conn, derr := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socketPath, Net: "unixgram"})
		if derr != nil {
			t.Fatalf("dialling: %v", derr)
		}
		body := "STATUS=" + strings.Repeat("x", c.size)
		if _, werr := conn.Write([]byte(body)); werr != nil {
			t.Fatalf("sending: %v", werr)
		}
		if cerr := conn.Close(); cerr != nil {
			t.Logf("closing the client: %v", cerr)
		}

		_, rerr := l.Recv()
		if !errs.HasCode(rerr, coreproc.CodeInvalidNotification) {
			t.Fatalf("Recv = %v, want INVALID_NOTIFICATION", rerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// closeLater registers l for teardown, taking it as an argument so the loop
// variable is not captured by the closure.
func closeLater(t *testing.T, l coreproc.Listener) {
	t.Helper()
	t.Cleanup(func() {
		if cerr := l.Close(); cerr != nil {
			t.Logf("closing the listener: %v", cerr)
		}
	})
}
