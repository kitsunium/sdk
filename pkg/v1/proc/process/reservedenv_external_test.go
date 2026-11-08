//go:build unix || windows

package process_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/process"
)

// TestStartRefusesReservedEnv pins #726 through the facade: a Spec.Env key of
// the SDK's spawn protocol, in any case, is refused with the exported
// ReservedEnv — errors.Is and its code both answer — and no process is
// returned.
func TestStartRefusesReservedEnv(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		key  string
	}
	tests := []tc{
		{"upper case", "__KITSUNIUM_SDK_PROC_TRAMPOLINE"},
		{"lower case", "__kitsunium_sdk_proc_hsfd"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		proc, err := process.Start(t.Context(), process.Spec{Path: "/bin/true", Env: []string{c.key + "=1"}})
		if proc != nil {
			t.Fatalf("Start returned a process for a refused spec")
		}
		if !errors.Is(err, process.ReservedEnv) || !errs.HasCode(err, process.CodeReservedEnv) {
			t.Fatalf("Start(Env %s=1) = %v, want ReservedEnv", c.key, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
