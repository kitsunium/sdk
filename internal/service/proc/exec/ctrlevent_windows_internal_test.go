//go:build windows

package exec

import (
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// TestConsoleEventIsBreakForEveryGroupSignal pins #933's mapping: SIGINT,
// SIGTERM, SIGHUP and SIGQUIT each reach a Setpgid child's group as
// CTRL_BREAK_EVENT, the one console event Windows delivers to one group —
// CTRL_C_EVENT to a group is accepted and received by nobody. SIGKILL has no
// console analogue: it stays TerminateProcess. On the base SIGINT mapped to
// CTRL_C_EVENT (0).
func TestConsoleEventIsBreakForEveryGroupSignal(t *testing.T) {
	t.Parallel()
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT} {
		if event, ok := consoleEvent(coreproc.Signal(sig)); !ok || event != ctrlBreakEvent {
			t.Errorf("consoleEvent(%v) = (%d, %v), want CTRL_BREAK_EVENT (%d)", sig, event, ok, ctrlBreakEvent)
		}
	}
	if _, ok := consoleEvent(coreproc.Signal(syscall.SIGKILL)); ok {
		t.Error("consoleEvent(SIGKILL) has an event, want TerminateProcess")
	}
}
