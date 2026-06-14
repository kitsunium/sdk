// Package sdnotify_test — black-box tests for the public sd_notify facade.
package sdnotify_test

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/sdnotify"
)

// TestNotifyNoopWhenUnset asserts the facade no-ops (nil) when NOTIFY_SOCKET is
// unset, delegating the libsystemd semantics intact.
func TestNotifyNoopWhenUnset(t *testing.T) {
	//: an empty NOTIFY_SOCKET counts as "unset"; t.Setenv restores it at test end.
	t.Setenv("NOTIFY_SOCKET", "")
	//: Ready must no-op silently.
	if err := sdnotify.Ready(); err != nil {
		//: a non-nil result violates the unset-socket contract.
		t.Fatalf("Ready() unset = %v, want nil", err)
	}
	//: Notify must no-op silently too.
	if err := sdnotify.Notify(map[string]string{"READY": "1"}); err != nil {
		//: report the unexpected error.
		t.Fatalf("Notify() unset = %v, want nil", err)
	}
}

// TestWatchdogInterval asserts the facade parses $WATCHDOG_USEC into a Duration.
func TestWatchdogInterval(t *testing.T) {
	//: set a 10s watchdog in microseconds.
	t.Setenv("WATCHDOG_USEC", "10000000")
	//: the facade must report the parsed interval.
	d, ok := sdnotify.WatchdogInterval()
	//: ok must be true for a valid value.
	if !ok {
		//: report the missing watchdog.
		t.Fatalf("WatchdogInterval() ok = false, want true")
	}
	//: the duration must be 10 seconds.
	if d != 10*time.Second {
		//: report the wrong duration.
		t.Fatalf("WatchdogInterval() = %v, want 10s", d)
	}
}

// TestWatchdogIntervalUnset asserts ok is false when $WATCHDOG_USEC is unset.
func TestWatchdogIntervalUnset(t *testing.T) {
	//: ensure the variable is absent.
	t.Setenv("WATCHDOG_USEC", "")
	//: an unset value disables the watchdog.
	_, ok := sdnotify.WatchdogInterval()
	//: ok must be false.
	if ok {
		//: report the unexpected enabled watchdog.
		t.Fatalf("WatchdogInterval() ok = true, want false when unset")
	}
}

// TestRoundTripThroughFacade exercises the full Listen/Notify/Recv path through
// the public surface on Linux (skipped elsewhere).
func TestRoundTripThroughFacade(t *testing.T) {
	//: the listener is Linux-only.
	if runtime.GOOS != "linux" {
		//: skip where unsupported.
		t.Skip("sd_notify listener is linux-only")
	}
	//: stand up the listener via the facade.
	l, path, err := sdnotify.Listen()
	//: degrade to a skip if the host cannot provide the socket.
	if err != nil {
		//: skip on hosts without the facility.
		t.Skipf("Listen unavailable: %v", err)
	}
	//: release the listener at the end.
	defer l.Close()
	//: point the notifier at the listener.
	t.Setenv("NOTIFY_SOCKET", path)
	//: send readiness.
	if rerr := sdnotify.Ready(); rerr != nil {
		//: a send failure aborts the test.
		t.Fatalf("Ready() = %v, want nil", rerr)
	}
	//: receive the credential-verified notification.
	n, err := l.Recv()
	//: a recv failure aborts the test.
	if err != nil {
		//: report the recv error.
		t.Fatalf("Recv() = %v, want nil", err)
	}
	//: the datagram must be ready and from this process.
	if !n.Ready() || n.SenderPID != os.Getpid() {
		//: report the contract violation.
		t.Fatalf("Recv() ready=%v senderPID=%d, want true and %d", n.Ready(), n.SenderPID, os.Getpid())
	}
}
