package codec

import (
	"strings"
	"sync/atomic"
	"testing"
)

// Test_indexAliases covers fresh-alias, idempotent, and conflict paths of
// the helper. Panic inspection is performed via callRecoverAliases.
func Test_indexAliases(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		existing  []string
		existName Format
		newAlias  string
		newName   Format
		kind      string
		wantPanic bool
	}
	tests := []tc{
		{"new alias stores silently", nil, "", "application/x-a", Format("a"), "MIME", false},
		{"same codec re-registers alias", []string{"application/x-b"}, Format("b"), "application/x-b", Format("b"), "MIME", false},
		{"conflicting codec panics", []string{"application/x-c"}, Format("c"), "application/x-c", Format("d"), "MIME", true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var dst atomic.Pointer[map[string]Format]
		//: seed the snapshot with the existing aliases via the same publish path.
		if len(tc.existing) > 0 {
			seed := make(map[string]Format, len(tc.existing))
			for _, a := range tc.existing {
				seed[strings.ToLower(a)] = tc.existName
			}
			dst.Store(&seed)
		}
		//: wrap the call so we can inspect panics inside a recover.
		panicked, r := callRecoverAliases(&dst, []string{tc.newAlias}, tc.newName, tc.kind)
		if panicked != tc.wantPanic {
			t.Errorf("%s: panic=%v wantPanic=%v (r=%v)", tc.name, panicked, tc.wantPanic, r)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// callRecoverAliases invokes indexAliases inside a recover so table-driven
// subtests can assert panic behaviour without using defer — t.Cleanup is
// not usable here because we want the panic-recovery to happen before the
// assertion runs.
func callRecoverAliases(dst *atomic.Pointer[map[string]Format], aliases []string, name Format, kind string) (panicked bool, recovered any) {
	//: classic recover pattern isolated in a helper so the caller stays flat.
	defer func() {
		//: capture the recover value inline.
		if r := recover(); r != nil {
			//: surface the panic to the caller.
			panicked = true
			recovered = r
		}
	}()
	//: execute the helper under observation.
	indexAliases(dst, aliases, name, kind)
	//: no panic path.
	return false, nil
}
