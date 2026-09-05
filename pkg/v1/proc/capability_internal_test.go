// Package proc — white-box tests for the capability matrix internals. The
// 120-rune Public ceiling and the name/GOOS table agreement are unreachable
// from a black-box test, and both are exactly the kind of thing that silently
// rots when a capability is added.
package proc

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	errs "github.com/kitsunium/sdk/pkg/v1/errs"
)

// Test_unsupportedError pins the typed error a MustSupport panic carries: the
// central code so a recover() can classify it, and a Public message that stays
// inside the wire-safe ceiling however many capabilities are missing.
func Test_unsupportedError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many capabilities to report as missing; the long case is what
		//: drives Public past maxPublicLen and into the fallback form.
		missing  []Capability
		wantList bool
	}
	tests := []tc{
		{"one capability", []Capability{CapCgroup}, true},
		{"two capabilities", []Capability{CapCgroup, CapRlimit}, true},
		{
			name: "every capability at once overflows the ceiling",
			missing: []Capability{
				CapProcessSpawn, CapRlimit, CapUmaskNiceOOM, CapCgroup,
				CapReaper, CapSignalRelay, CapSdNotify, CapSocketActivation,
			},
			wantList: false,
		},
		{"an out-of-range capability", []Capability{Capability(0x7BAD)}, true},
		{"none at all", nil, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := unsupportedError(c.missing)
		if err == nil {
			t.Fatal("unsupportedError = nil, want a typed error")
		}
		//: the code is what a recover() classifies on; a divergence here would
		//: make the panic path indistinguishable from an untyped one.
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("unsupportedError = %v, want code UNSUPPORTED_PLATFORM", err)
		}

		public := errs.PublicOf(err)
		if len([]rune(public)) > maxPublicLen {
			t.Errorf("Public is %d runes, over the %d ceiling: %q",
				len([]rune(public)), maxPublicLen, public)
		}
		//: the platform always survives truncation — it is what makes the
		//: message actionable at all.
		if !strings.Contains(public, runtime.GOOS) {
			t.Errorf("Public %q does not name the platform", public)
		}
		//: past the ceiling the names are dropped, and only then.
		named := strings.Contains(public, "[")
		if named != c.wantList {
			t.Errorf("Public names the capabilities = %v, want %v: %q", named, c.wantList, public)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_capabilityGOOS pins the two tables against each other. They are edited
// separately, so a capability added to one and forgotten in the other would
// otherwise surface only as a silent "unsupported everywhere" at runtime.
func Test_capabilityGOOS(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, c Capability) {
		t.Helper()
		targets, ok := capabilityGOOS[c]
		if !ok {
			t.Fatalf("%s has a name but no GOOS row: it would be unsupported everywhere", c)
		}
		if len(targets) == 0 {
			t.Errorf("%s has an empty GOOS row", c)
		}
		//: linux is the SDK's reference target; a capability supported nowhere
		//: on it is almost certainly a typo in the row.
		if !slices.Contains(targets, "linux") {
			t.Errorf("%s lists no linux backend: %v", c, targets)
		}
	}
	type tc struct {
		name string
		cap  Capability
	}
	tests := make([]tc, 0, len(capabilityNames))
	for i, name := range capabilityNames {
		tests = append(tests, tc{name, Capability(i)})
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.cap)
		})
	}
}
