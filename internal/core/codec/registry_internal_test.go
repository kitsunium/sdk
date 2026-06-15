package codec

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
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
		//: issue #36 — a parameter-only alias now normalises to its bare media
		//: type, so registering "application/base64;url=true" after a distinct
		//: codec owns "application/base64" is a loud collision, not a silent,
		//: lookup-unreachable coexistence.
		{"param-only alias collides with bare form", []string{"application/base64"}, Format("base64"), "application/base64;url=true", Format("base64url"), "MIME", true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var dst snapshot.Value[map[string]Format]
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
func callRecoverAliases(dst *snapshot.Value[map[string]Format], aliases []string, name Format, kind string) (panicked bool, recovered any) {
	//: classic recover pattern isolated in a helper so the caller stays flat.
	defer func() {
		//: capture the recover value inline.
		if r := recover(); r != nil {
			//: surface the panic to the caller.
			panicked = true
			recovered = r
		}
	}()
	//: execute the helper under observation (MIME normalizer — every case here
	//: is a MIME alias; param-free inputs key identically under normalizeMIME).
	indexAliases(dst, aliases, name, kind, normalizeMIME)
	//: no panic path.
	return false, nil
}

// ResetForTest clears the process-wide codec registry and its MIME / extension
// alias indexes back to the empty state. Exported from this white-box test file
// so the external test package (codec_test) can isolate registry mutations: the
// registry is package-global and `go test -count=N` reuses the process (package
// state is NOT re-initialised between iterations), so a test that calls Register
// must reset first or a later iteration panics on a duplicate Name. Test-only.
func ResetForTest() {
	//: store a nil snapshot into each Value; loadRegistry / loadAliasIndex then
	//: report empty (Load returns nil → callers see a clean miss).
	registry.Store(nil)
	//: MIME alias index back to empty.
	mimeIndex.Store(nil)
	//: extension alias index back to empty.
	extIndex.Store(nil)
}
