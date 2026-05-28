package codec_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
)

// mockCodec is a minimal Codec impl used only by the registry tests.
type mockCodec struct {
	name string
	mime []string
	ext  []string
}

func (m *mockCodec) Name() string                       { return m.name }
func (m *mockCodec) MIMETypes() []string                { return m.mime }
func (m *mockCodec) Extensions() []string               { return m.ext }
func (m *mockCodec) Marshal(v any) ([]byte, error)      { return []byte(m.name), nil }
func (m *mockCodec) Unmarshal(data []byte, v any) error { return nil }

func TestRegister(t *testing.T) {
	//: start from a clean registry so this test is isolated and survives
	//: `go test -count=N` (the process — and package-global registry — persists
	//: across iterations).
	codec.ResetForTest()
	tests := []struct {
		name string
		arg  *mockCodec
	}{
		{
			name: "valid codec is registered",
			arg: &mockCodec{
				name: "mock-register-1",
				mime: []string{"application/x-mock-1"},
				ext:  []string{".mock1"},
			},
		},
		{
			name: "second slot, distinct codec",
			arg: &mockCodec{
				name: "mock-register-2",
				mime: []string{"application/x-mock-2"},
				ext:  []string{".mock2"},
			},
		},
	}
	runCase := func(t *testing.T, mc *mockCodec) {
		t.Helper()
		codec.Register(mc)
		if _, ok := codec.Lookup(codec.Format(mc.name)); !ok {
			t.Errorf("Lookup(%q) failed after Register", mc.name)
		}
	}
	for _, tc := range tests {
		//: sequential — Register mutates the process-wide registry.
		t.Run(tc.name, func(t *testing.T) {
			runCase(t, tc.arg)
		})
	}
}

func TestRegisterPanics(t *testing.T) {
	//: clean slate so the duplicate-Name case panics on its OWN second
	//: registration, not on a leftover from a prior test or `-count` iteration.
	codec.ResetForTest()
	tests := []struct {
		name string
		run  func()
	}{
		{"nil panics", func() { codec.Register(nil) }},
		{
			"duplicate Name panics",
			func() {
				dup := &mockCodec{name: "mock-dup", mime: []string{"m/x"}, ext: []string{".xdup"}}
				codec.Register(dup)
				codec.Register(dup)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("%s: expected panic, got none", tc.name)
				}
			}()
			tc.run()
		})
	}
}

func TestLookup(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     codec.Format
		wantOK bool
	}
	tests := []tc{
		{"empty misses", codec.Format(""), false},
		{"unregistered misses", codec.Format("absent-zzz"), false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if _, ok := codec.Lookup(c.in); ok != c.wantOK {
			t.Errorf("%s: ok=%v want %v", c.name, ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestLookupMIME(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     string
		wantOK bool
	}
	tests := []tc{
		{"empty misses", "", false},
		{"unknown misses", "application/x-absent", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if _, ok := codec.LookupMIME(c.in); ok != c.wantOK {
			t.Errorf("%s: ok=%v want %v", c.name, ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestLookupExt(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     string
		wantOK bool
	}
	tests := []tc{
		{"empty misses", "", false},
		{"unknown misses", ".absent", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if _, ok := codec.LookupExt(c.in); ok != c.wantOK {
			t.Errorf("%s: ok=%v want %v", c.name, ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestAvailable(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{
		{"list is sorted ascending"},
	}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		got := codec.Available()
		for i := 1; i < len(got); i++ {
			if got[i-1] > got[i] {
				t.Errorf("Available() not sorted: %v", got)
				return
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRegistryHitPaths covers the success arms the miss-only tables above
// never reach: a resolved Lookup / LookupMIME / LookupExt, MIME-parameter
// stripping, the malformed-MIME manual fallback, and case-insensitive
// extension matching. Sequential — it mutates the process-wide registry.
func TestRegistryHitPaths(t *testing.T) {
	//: clean slate so the assertions below see exactly the one codec we add.
	codec.ResetForTest()
	mc := &mockCodec{
		name: "cov-hit",
		mime: []string{"application/x-cov"},
		ext:  []string{".cov"},
	}
	codec.Register(mc)
	type tc struct {
		name   string
		lookup func() (codec.Codec, bool)
		wantOK bool
	}
	tests := []tc{
		{"Lookup resolves the registered Format", func() (codec.Codec, bool) {
			return codec.Lookup(codec.Format("cov-hit"))
		}, true},
		{"LookupMIME resolves the bare media type", func() (codec.Codec, bool) {
			return codec.LookupMIME("application/x-cov")
		}, true},
		{"LookupMIME strips the charset parameter", func() (codec.Codec, bool) {
			return codec.LookupMIME("application/x-cov; charset=utf-8")
		}, true},
		{"LookupMIME on a malformed header falls back and resolves", func() (codec.Codec, bool) {
			return codec.LookupMIME("application/x-cov; =bad")
		}, true},
		{"LookupExt resolves the registered extension", func() (codec.Codec, bool) {
			return codec.LookupExt(".cov")
		}, true},
		{"LookupExt matches case-insensitively", func() (codec.Codec, bool) {
			return codec.LookupExt(".COV")
		}, true},
		//: index is seeded but the key is absent — exercises the !found arm
		//: that the m==nil empty-registry test never reaches.
		{"LookupMIME misses an absent key on a populated index", func() (codec.Codec, bool) {
			return codec.LookupMIME("application/x-absent")
		}, false},
		{"LookupExt misses an absent key on a populated index", func() (codec.Codec, bool) {
			return codec.LookupExt(".absent")
		}, false},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; sequential — shares the process-wide registry seeded above.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if _, ok := tc.lookup(); ok != tc.wantOK {
			t.Errorf("%s: ok=%v want %v", tc.name, ok, tc.wantOK)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
	//: Available must now surface the freshly registered Format.
	found := false
	for _, f := range codec.Available() {
		//: scan for our codec among the sorted Formats.
		if f == codec.Format("cov-hit") {
			found = true
		}
	}
	if !found {
		t.Errorf("Available() = %v, want it to contain cov-hit", codec.Available())
	}
}

// TestRegistryEmptyMisses covers the nil-snapshot arms of every reader:
// before any Register (the documented init-order edge), loadRegistry and
// loadAliasIndex return nil so each lookup is a clean miss and Available is
// nil. Sequential — parallel tables only resume after the sequential phase,
// so resetting the global here never races a concurrent reader.
func TestRegistryEmptyMisses(t *testing.T) {
	//: drive all three snapshots back to nil to hit the absence branches.
	codec.ResetForTest()
	type tc struct {
		name   string
		lookup func() (codec.Codec, bool)
	}
	tests := []tc{
		{"Lookup misses on empty registry", func() (codec.Codec, bool) {
			return codec.Lookup(codec.Format("cov-hit"))
		}},
		{"LookupMIME misses on empty index", func() (codec.Codec, bool) {
			return codec.LookupMIME("application/x-cov")
		}},
		{"LookupExt misses on empty index", func() (codec.Codec, bool) {
			return codec.LookupExt(".cov")
		}},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; every reader must report a miss against the nil snapshot.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if _, ok := tc.lookup(); ok {
			t.Errorf("%s: ok=true, want false on empty registry", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
	//: Available must return the documented nil slice when nothing is registered.
	if got := codec.Available(); got != nil {
		t.Errorf("Available() = %v, want nil on empty registry", got)
	}
}
