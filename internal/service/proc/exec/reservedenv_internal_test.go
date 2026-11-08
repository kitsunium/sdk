// Package exec — the refusal of the spawn protocol's own variables (#726).
package exec

import (
	"errors"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// reservedEnvCases is one case per variable the spawn protocol sets, by its
// literal name: TestReservedEnvKeysListed fails when the package declares a
// variable with the prefix that this table does not name.
var reservedEnvCases = []string{
	"__KITSUNIUM_SDK_PROC_TRAMPOLINE",
	"__KITSUNIUM_SDK_PROC_CGROUP",
	"__KITSUNIUM_SDK_PROC_HSFD",
	"__KITSUNIUM_SDK_PROC_PIDENV",
	"__KITSUNIUM_SDK_PROC_SANDBOX",
}

// TestValidateSpecRefusesReservedEnv pins #726: a Spec.Env key of the spawn
// protocol is refused with ReservedEnv before any fork, whatever its case and
// whichever key it is, and a key that only resembles the prefix is accepted.
//
// The child reads the FIRST of two equal keys and Spec.Env precedes the
// variables the spawn appends, so an accepted key would replace the
// trampoline's own: its payload, the cgroup it writes its pid into, the
// descriptor it reports on.
func TestValidateSpecRefusesReservedEnv(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		env     []string
		wantKey string
	}
	tests := make([]tc, 0, 2*len(reservedEnvCases)+6)
	for _, key := range reservedEnvCases {
		tests = append(tests,
			tc{name: key, env: []string{"A=1", key + "=x"}, wantKey: key},
			tc{name: strings.ToLower(key), env: []string{strings.ToLower(key) + "=x"}, wantKey: strings.ToLower(key)},
		)
	}
	tests = append(tests,
		//: any name with the prefix, not only the three in use today.
		tc{name: "a future key", env: []string{"__KITSUNIUM_SDK_PROC_NEXT=1"}, wantKey: "__KITSUNIUM_SDK_PROC_NEXT"},
		tc{name: "a mixed-case prefix", env: []string{"__Kitsunium_Sdk_Proc_Cgroup=/x"}, wantKey: "__Kitsunium_Sdk_Proc_Cgroup"},
		tc{name: "the bare prefix", env: []string{"__KITSUNIUM_SDK_PROC_="}, wantKey: "__KITSUNIUM_SDK_PROC_"},
		//: neighbours: accepted.
		tc{name: "no separator after PROC", env: []string{"__KITSUNIUM_SDK_PROCX=1"}},
		tc{name: "no leading underscores", env: []string{"KITSUNIUM_SDK_PROC_X=1"}},
		tc{name: "the prefix in a value", env: []string{"X=__KITSUNIUM_SDK_PROC_CGROUP=/x"}},
	)
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := validateSpec(coreproc.Spec{Path: "/bin/true", Env: c.env})
		if c.wantKey == "" {
			if err != nil {
				t.Fatalf("validateSpec(Env %q) = %v, want nil", c.env, err)
			}
			return
		}
		if !errors.Is(err, coreproc.ReservedEnv) {
			t.Fatalf("validateSpec(Env %q) = %v, want ReservedEnv", c.env, err)
		}
		var e *errs.Error
		if !errors.As(err, &e) {
			t.Fatalf("validateSpec(Env %q) = %T, want *errs.Error", c.env, err)
		}
		if got := fieldValue(e, "key"); got != c.wantKey {
			t.Errorf("the refusal names key %q, want %q", got, c.wantKey)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// fieldValue returns the string value of field name on e, empty when absent.
func fieldValue(e *errs.Error, name string) string {
	for _, f := range e.Fields() {
		if f.Key() == name {
			return f.StringValue()
		}
	}
	return ""
}
