// Package exec — the pre-flight spec check.
package exec

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_validateSpec pins the one structural invariant checked before any OS
// work happens.
//
// An empty Path is the only thing that can never fork/exec whatever the host
// looks like, so it is refused here; everything else — a missing binary, an
// unknown user, an unmappable resource — is resolved during the spawn and gets
// its own typed error. Rejecting more here would move host-specific decisions
// into a portable check.
func Test_validateSpec(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		spec    coreproc.Spec
		wantErr bool
	}
	tests := []tc{
		{name: "a bare path", spec: coreproc.Spec{Path: "/bin/true"}},
		{name: "a path with arguments", spec: coreproc.Spec{Path: "/bin/sh", Args: []string{"sh", "-c", "true"}}},
		{
			//: a path that does not exist is NOT refused here: whether it
			//: resolves is a host fact the spawn discovers.
			name: "a path that does not exist",
			spec: coreproc.Spec{Path: "/definitely/not/here"},
		},
		{
			name: "a fully populated spec",
			spec: coreproc.Spec{
				Path: "/bin/true", Dir: "/tmp", Env: []string{"A=1"},
				User: "nobody", Setpgid: true, Setsid: true,
			},
		},
		{name: "the zero spec", spec: coreproc.Spec{}, wantErr: true},
		{
			name:    "everything set except the path",
			spec:    coreproc.Spec{Args: []string{"sh"}, Dir: "/tmp", Setpgid: true},
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := validateSpec(c.spec)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeInvalidSpec) {
				t.Fatalf("validateSpec(%s) = %v, want INVALID_SPEC", c.name, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("validateSpec(%s) = %v, want nil", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
