package codec

import (
	"errors"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
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
		var dst plugin.Registry[string, Format]
		//: seed the index with the existing aliases, keyed as Register keys them.
		for _, a := range tc.existing {
			dst.Publish(strings.ToLower(a), tc.existName)
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
func callRecoverAliases(dst *plugin.Registry[string, Format], aliases []string, name Format, kind string) (panicked bool, recovered any) {
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
// must reset first or a later iteration panics on a duplicate Name. Test-only,
// and only from a test that runs alone: it replaces the tables rather than
// storing into them, which nothing may race.
func ResetForTest() {
	//: a fresh, empty Format table; Lookup then reports a clean miss.
	registry = plugin.Registry[Format, Codec]{}
	//: MIME alias index back to empty.
	mimeIndex = plugin.Registry[string, Format]{}
	//: extension alias index back to empty.
	extIndex = plugin.Registry[string, Format]{}
}

// Test_publish_conflictIsTyped proves both conflicts the registry can meet are
// SDK errors with the duplicate-registration code (rule 2): an operator, a
// test or a later registry that returns them instead of panicking can match
// them with errs.HasCode rather than by parsing text.
func Test_publish_conflictIsTyped(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	first := &stubCodec{name: "typed-a"}
	if err := publishCodec("typed-a", first); err != nil {
		t.Fatalf("the first publish = %v, want nil", err)
	}
	var aliases plugin.Registry[string, Format]
	if err := publishAlias(&aliases, "m/typed", "typed-a", "MIME", "m/typed"); err != nil {
		t.Fatalf("the first alias = %v, want nil", err)
	}
	conflicts := map[string]error{
		"a taken Name":  publishCodec("typed-a", &stubCodec{name: "typed-a"}),
		"a taken alias": publishAlias(&aliases, "m/typed", "typed-b", "MIME", "m/typed"),
	}
	for name, err := range conflicts {
		if !errs.HasCode(err, CodeDuplicateRegistration) || !errors.Is(err, DuplicateRegistration) {
			t.Errorf("%s: conflict = %v, want the typed DuplicateRegistration", name, err)
		}
	}
}

// Test_publishCodec_isStrictOnTheName pins the one rule this registry keeps
// that the kernel table's Publish does not: a Format registers once, so the
// SAME codec claimed twice is a conflict here, where an alias, and every other
// core registry, would accept it again as a no-op.
func Test_publishCodec_isStrictOnTheName(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	same := &stubCodec{name: "strict"}
	if err := publishCodec("strict", same); err != nil {
		t.Fatalf("the first publish = %v, want nil", err)
	}
	if err := publishCodec("strict", same); !errs.HasCode(err, CodeDuplicateRegistration) {
		t.Fatalf("the same codec published twice = %v, want DUPLICATE_REGISTRATION", err)
	}
	//: the refused second claim left the first in place.
	if got, ok := Lookup("strict"); !ok || got != same {
		t.Fatalf("Lookup after the refusal = %v, %v; want the first codec", got, ok)
	}
}

// Test_publishAlias_namesTheOwner pins what a conflict on an alias reports: the
// Format already holding the key and the one asking, read by the same step that
// refused the claim — the fields an operator needs to find the two imports.
func Test_publishAlias_namesTheOwner(t *testing.T) {
	t.Parallel()
	var aliases plugin.Registry[string, Format]
	if err := publishAlias(&aliases, "m/owned", "holder", "MIME", "m/owned"); err != nil {
		t.Fatalf("the first alias = %v, want nil", err)
	}
	//: the same Format claiming its own alias again is the accepted no-op.
	if err := publishAlias(&aliases, "m/owned", "holder", "MIME", "m/owned"); err != nil {
		t.Fatalf("the holder re-claiming its alias = %v, want nil", err)
	}
	err := publishAlias(&aliases, "m/owned", "asker", "MIME", "M/Owned")
	text := conflictText(err)
	for _, want := range []string{`owner="holder"`, `requester="asker"`, `alias="M/Owned"`, `kind="MIME"`} {
		if !strings.Contains(text, want) {
			t.Errorf("conflictText = %q, want it to say %s", text, want)
		}
	}
}

// stubCodec is the smallest Codec the white-box registry tests publish.
type stubCodec struct{ name string }

func (s *stubCodec) Name() string                    { return s.name }
func (s *stubCodec) MIMETypes() []string             { return nil }
func (s *stubCodec) Extensions() []string            { return nil }
func (s *stubCodec) Marshal(any) ([]byte, error)     { return nil, nil }
func (s *stubCodec) Unmarshal(_ []byte, _ any) error { return nil }
