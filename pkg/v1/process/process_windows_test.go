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
	//: the backend spawns, rather than refusing the platform.
	if err != nil {
		t.Fatalf("Start on windows = %v, want a supervised child", err)
	}
	exit, werr := p.Wait()
	//: the child is reaped through the handle.
	if werr != nil {
		t.Fatalf("Wait = %v", werr)
	}
	//: and its own exit code comes back.
	if exit.Code != 7 {
		t.Fatalf("exit code = %d, want 7", exit.Code)
	}
}

// TestStartRefusesAUnixOnlyFieldOnWindows pins the half of the contract that is
// a refusal: a Spec asking for something Windows has no mechanism for is
// refused, never spawned with the request silently dropped.
func TestStartRefusesAUnixOnlyFieldOnWindows(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		spec process.Spec
	}
	tests := []tc{
		{name: "a umask", spec: process.Spec{Path: shell(), Args: []string{"cmd", "/c", "exit 0"}, Umask: new(0o077)}},
		{name: "POSIX credentials", spec: process.Spec{Path: shell(), Args: []string{"cmd", "/c", "exit 0"}, User: "nobody"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p, err := process.Start(t.Context(), c.spec)
		//: the typed floor, not a spawn with the field dropped.
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("Start with %s on windows = %v, want UNSUPPORTED_PLATFORM", c.name, err)
		}
		//: and nothing was started behind the refusal.
		if p != nil {
			t.Fatal("a refused Start still returned a process")
		}
	}
	//: one subtest per Unix-only field.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
