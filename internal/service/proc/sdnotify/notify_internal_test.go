// Package sdnotify — the notifier's error wrapper.
package sdnotify

import (
	"errors"
	"os"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_wrapNotify pins the send-failure wrapper. The socket value rides along
// because $NOTIFY_SOCKET is operator-supplied configuration, and "could not send
// the sd_notify datagram" without saying WHERE leaves nothing to check.
func Test_wrapNotify(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		cause  error
		socket string
	}
	tests := []tc{
		{"a socket that does not exist", os.ErrNotExist, "/run/systemd/notify"},
		{"a permission denial", os.ErrPermission, "/run/systemd/notify"},
		{"an abstract-namespace socket", os.ErrClosed, "@sdk/notify"},
		{"a nil cause", nil, "/run/systemd/notify"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := wrapNotify(c.cause, c.socket)

		if !errs.HasCode(err, coreproc.CodeNotifyFailed) {
			t.Fatalf("wrapNotify = %v, want NOTIFY_FAILED", err)
		}
		//: EX_OSERR says the fault was the operating system's, which is what
		//: distinguishes an unreachable socket from a malformed datagram.
		if got := errs.ExitCodeOf(err); got != exitOSErr {
			t.Errorf("exit code = %d, want %d", got, exitOSErr)
		}
		//: the socket must be named, or an operator cannot tell which of
		//: several configured paths was wrong.
		var named bool
		for _, f := range errs.FieldsOf(err) {
			if f.Key() == "socket" && f.StringValue() == c.socket {
				named = true
			}
		}
		if !named {
			t.Errorf("the error does not name the socket: %v", errs.FieldsOf(err))
		}
		//: the cause stays reachable so the reason is diagnosable.
		if c.cause != nil && !errors.Is(err, c.cause) {
			t.Errorf("wrapNotify = %v, want it to wrap %v", err, c.cause)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
