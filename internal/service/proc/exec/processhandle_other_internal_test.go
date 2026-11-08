//go:build unix && !(linux && !mips && !mipsle && !mips64 && !mips64le)

package exec

import (
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_ProcessHandle_refused pins #928's off-Linux row: the handle reports no
// kernel handle, SignalHandle and Open are UnsupportedPlatform.
func Test_ProcessHandle_refused(t *testing.T) {
	t.Parallel()
	p, err := Start(t.Context(), coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "exit 0"}})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	defer func() { _, _ = p.Wait() }()
	ph := p.(coreproc.ProcessHandle)
	if f, ok := ph.Handle(); ok || f != nil {
		t.Errorf("Handle() = (%v, %v), want (nil, false)", f, ok)
	}
	if err := ph.SignalHandle(coreproc.Signal(syscall.SIGTERM)); !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Errorf("SignalHandle = %v, want UnsupportedPlatform", err)
	}
	if _, err := Open(p.PID()); !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Errorf("Open = %v, want UnsupportedPlatform", err)
	}
}
