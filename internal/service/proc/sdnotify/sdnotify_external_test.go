// Package sdnotify_test — black-box acceptance tests for the sd_notify service.
package sdnotify_test

import (
	"os"
	"runtime"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	sdnotify "github.com/kitsunium/sdk/internal/service/proc/sdnotify"
)

// TestNotifyNoopWhenUnset asserts that with $NOTIFY_SOCKET unset every notifier
// func is a no-op returning nil (libsystemd semantics), not an error.
func TestNotifyNoopWhenUnset(t *testing.T) {
	//: an empty NOTIFY_SOCKET counts as "unset" for the no-op contract; t.Setenv
	//: restores the prior value at test end without an explicit Unsetenv.
	t.Setenv("NOTIFY_SOCKET", "")
	//: each notifier entry point must no-op silently when the socket is unset.
	calls := []struct {
		name string
		fn   func() error
	}{
		//: Ready shorthand.
		{name: "Ready", fn: sdnotify.Ready},
		//: Reloading shorthand.
		{name: "Reloading", fn: sdnotify.Reloading},
		//: Stopping shorthand.
		{name: "Stopping", fn: sdnotify.Stopping},
		//: Watchdog shorthand.
		{name: "Watchdog", fn: sdnotify.Watchdog},
		//: Status shorthand.
		{name: "Status", fn: func() error { return sdnotify.Status("x") }},
		//: MainPID shorthand.
		{name: "MainPID", fn: func() error { return sdnotify.MainPID(1) }},
		//: raw Notify.
		{name: "Notify", fn: func() error { return sdnotify.Notify(map[string]string{"READY": "1"}) }},
	}
	//: every call must return nil with no socket present.
	for _, c := range calls {
		//: run the notifier and assert the no-op nil result.
		if err := c.fn(); err != nil {
			//: a non-nil error violates the unset-socket contract.
			t.Fatalf("%s with unset NOTIFY_SOCKET = %v, want nil no-op", c.name, err)
		}
	}
}

// TestNotifyFailsWhenSocketBroken asserts a set-but-unreachable $NOTIFY_SOCKET
// surfaces NotifyFailed rather than silently succeeding.
func TestNotifyFailsWhenSocketBroken(t *testing.T) {
	//: point at a path that does not exist so DialUnix fails.
	t.Setenv("NOTIFY_SOCKET", "/nonexistent/sdnotify/socket")
	//: the send must fail with the typed NotifyFailed sentinel.
	err := sdnotify.Ready()
	//: an unreachable but configured socket is a real error.
	if !errs.HasCode(err, coreproc.CodeNotifyFailed) {
		//: report the wrong/absent error.
		t.Fatalf("Ready() with broken socket = %v, want NotifyFailed", err)
	}
}

// TestListenUnsupportedOffLinux asserts the listener returns UnsupportedPlatform
// on non-Linux platforms; on Linux this test is skipped (the round-trip below
// covers it).
func TestListenUnsupportedOffLinux(t *testing.T) {
	t.Parallel()
	//: the credential listener only exists on Linux.
	if runtime.GOOS == "linux" {
		//: skip on Linux where Listen is supported.
		t.Skip("listener is supported on linux; covered by the round-trip test")
	}
	//: off Linux, Listen must degrade to UnsupportedPlatform, not panic.
	_, _, err := sdnotify.Listen()
	//: assert the typed UnsupportedPlatform sentinel.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: report the wrong/absent error.
		t.Fatalf("Listen() off linux = %v, want UnsupportedPlatform", err)
	}
}

// TestRoundTrip is the headline acceptance test (Linux only): Listen(), set
// NOTIFY_SOCKET to its path, call Ready(), then Recv() observes Ready()==true and
// SenderPID==os.Getpid() — the kernel-verified SO_PASSCRED credential.
func TestRoundTrip(t *testing.T) {
	//: the credential round-trip is Linux-specific.
	if runtime.GOOS != "linux" {
		//: skip where the listener is unavailable.
		t.Skip("sd_notify listener (SO_PASSCRED) is linux-only")
	}
	//: stand up the supervisor-side socket.
	l, path, err := sdnotify.Listen()
	//: an unprivileged container can still bind a unixgram + SO_PASSCRED; if not,
	//: degrade to a skip rather than a hard failure.
	if err != nil {
		//: still assert the contract: the error must be the typed ListenFailed.
		if !errs.HasCode(err, coreproc.CodeListenFailed) {
			//: an unexpected error type is a real failure.
			t.Fatalf("Listen() = %v, want nil or ListenFailed", err)
		}
		//: skip the rest when the host cannot provide the socket.
		t.Skipf("Listen unavailable on this host: %v", err)
	}
	//: always release the listener.
	defer l.Close()
	//: a child reads NOTIFY_SOCKET; point the notifier at our listener.
	t.Setenv("NOTIFY_SOCKET", path)
	//: send READY=1 from this very process.
	if rerr := sdnotify.Ready(); rerr != nil {
		//: a send failure aborts the round-trip.
		t.Fatalf("Ready() = %v, want nil", rerr)
	}
	//: receive and parse the datagram with its credential.
	n, err := l.Recv()
	//: a recv failure aborts the round-trip.
	if err != nil {
		//: report the recv error.
		t.Fatalf("Recv() = %v, want nil", err)
	}
	//: the datagram must carry READY=1.
	if !n.Ready() {
		//: report the missing ready flag.
		t.Fatalf("Recv().Ready() = false, want true (state=%v)", n.State)
	}
	//: the kernel-verified sender PID must be our own.
	if n.SenderPID != os.Getpid() {
		//: report the credential mismatch.
		t.Fatalf("SenderPID = %d, want %d (this process)", n.SenderPID, os.Getpid())
	}
}

// TestRoundTripStatusMainPID exercises a richer datagram through the Linux
// listener: STATUS and MAINPID are lifted into typed fields.
func TestRoundTripStatusMainPID(t *testing.T) {
	//: Linux-only, like the headline round-trip.
	if runtime.GOOS != "linux" {
		//: skip where the listener is unavailable.
		t.Skip("sd_notify listener (SO_PASSCRED) is linux-only")
	}
	//: stand up the listener.
	l, path, err := sdnotify.Listen()
	//: degrade to a skip if the host cannot provide the socket.
	if err != nil {
		//: skip the test on hosts without the facility.
		t.Skipf("Listen unavailable on this host: %v", err)
	}
	//: release the listener at the end.
	defer l.Close()
	//: aim the notifier at the listener.
	t.Setenv("NOTIFY_SOCKET", path)
	//: send a multi-field datagram.
	if nerr := sdnotify.Notify(map[string]string{"STATUS": "serving", "MAINPID": "12345"}); nerr != nil {
		//: a send failure aborts the test.
		t.Fatalf("Notify() = %v, want nil", nerr)
	}
	//: receive and parse it.
	n, err := l.Recv()
	//: a recv failure aborts the test.
	if err != nil {
		//: report the recv error.
		t.Fatalf("Recv() = %v, want nil", err)
	}
	//: STATUS must be lifted verbatim.
	if n.Status != "serving" {
		//: report the wrong status.
		t.Fatalf("Status = %q, want %q", n.Status, "serving")
	}
	//: MAINPID must be parsed into the typed field.
	if n.MainPID != 12345 {
		//: report the wrong MainPID.
		t.Fatalf("MainPID = %d, want 12345", n.MainPID)
	}
}
