package notify_test

import (
	"path/filepath"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/systemd/notify"
)

// TestNotifyRefusedOnWindows pins the matrix's answer for CapSdNotify on
// Windows: a NOTIFY_SOCKET named there is refused with UnsupportedPlatform,
// and an unset one stays the silent no-op it is everywhere.
func TestNotifyRefusedOnWindows(t *testing.T) {
	//: not parallel — it sets $NOTIFY_SOCKET.
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "notify.sock"))
	//: a named socket cannot be reached without SOCK_DGRAM.
	if err := notify.Ready(); !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Ready() with NOTIFY_SOCKET set = %v, want UNSUPPORTED_PLATFORM", err)
	}
	t.Setenv("NOTIFY_SOCKET", "")
	//: unsupervised: nothing to send, nothing refused.
	if err := notify.Ready(); err != nil {
		t.Fatalf("Ready() with NOTIFY_SOCKET unset = %v, want nil", err)
	}
}
