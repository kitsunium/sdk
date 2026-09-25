//go:build windows

// Package process_test — the facade contract on Windows, where Start has a
// CreateProcess backend: a real child spawned and reaped through the public
// names, and a Unix-only Spec field refused with UnsupportedPlatform rather than
// dropped. process_other_test.go asserted the blanket refusal here until the
// first Windows run of the whole suite showed the backend it had missed.
package process_test

import (
	"os"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/process"
)

// shell is cmd.exe, which every Windows host carries: ComSpec names it, and the
// system32 path is the documented default.
func shell() string {
	//: ComSpec is set on every Windows host.
	if cs := os.Getenv("ComSpec"); cs != "" {
		return cs
	}
	//: the documented default location.
	return `C:\Windows\System32\cmd.exe`
}

// TestStartSupervisesAChildOnWindows spawns `cmd /c exit 7` through the facade
// and reads the exit code back: the whole Start → Wait path, on the real kernel.
func TestStartSupervisesAChildOnWindows(t *testing.T) {
	t.Parallel()
	p, err := process.Start(t.Context(), process.Spec{Path: shell(), Args: []string{"cmd", "/c", "exit 7"}})
	if err != nil {
		t.Fatalf("Start on windows = %v, want a supervised child", err)
	}
	exit, werr := p.Wait()
	if werr != nil {
		t.Fatalf("Wait = %v", werr)
	}
	if exit.Code != 7 {
		t.Fatalf("exit code = %d, want 7", exit.Code)
	}
}

// TestStartRefusesAUnixOnlyFieldOnWindows pins the half of the contract that is
// a refusal: a Spec asking for something Windows has no mechanism for is
// refused, never spawned with the request silently dropped.
func TestStartRefusesAUnixOnlyFieldOnWindows(t *testing.T) {
	t.Parallel()
	umask := 0o077
	tests := []struct {
		name string
		spec process.Spec
	}{
		{name: "a umask", spec: process.Spec{Path: shell(), Args: []string{"cmd", "/c", "exit 0"}, Umask: &umask}},
		{name: "POSIX credentials", spec: process.Spec{Path: shell(), Args: []string{"cmd", "/c", "exit 0"}, User: "nobody"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, err := process.Start(t.Context(), tt.spec)
			if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
				t.Fatalf("Start with %s on windows = %v, want UNSUPPORTED_PLATFORM", tt.name, err)
			}
			if p != nil {
				t.Fatal("a refused Start still returned a process")
			}
		})
	}
}
