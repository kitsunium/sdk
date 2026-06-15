package codec_test

import (
	"fmt"
	"strconv"
	"strings"
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
		//: the second MIME carries a parameter — issue #36: registration must
		//: normalise it to the bare media type so LookupMIME (which strips
		//: parameters) can reach it. Pre-fix, registration stored the raw
		//: "…;v=1" key and the bare-type lookup missed.
		mime: []string{"application/x-cov", "application/x-cov-param;v=1"},
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
		//: issue #36 — a MIME registered WITH a parameter is reachable via its
		//: bare media type because registration normalises the key the same way
		//: LookupMIME does. Pre-fix this missed (raw "…;v=1" key was indexed).
		{"LookupMIME reaches a param-registered MIME via its bare type", func() (codec.Codec, bool) {
			return codec.LookupMIME("application/x-cov-param")
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

// recoverPanicMessage runs fn and returns the recovered panic value rendered
// as a string. Register panics with a string payload by design (the dotted-quad
// boot message), so the rendered form IS the contract under audit for findings
// V16 / V18 — there is no *errs.Error value to route on.
func recoverPanicMessage(t *testing.T, fn func()) (msg string) {
	t.Helper()
	//: classic recover isolated in a helper so the assertion stays flat.
	defer func() {
		//: capture and stringify the panic payload for inspection.
		if r := recover(); r != nil {
			//: %v renders both string and error payloads uniformly.
			msg = fmt.Sprintf("%v", r)
		}
	}()
	//: execute under observation.
	fn()
	//: no panic — empty result signals the missing-panic failure to the caller.
	return ""
}

// TestRegister_NilUsesCodecNilCode is the V18 regression: Register(nil) must
// report the dedicated CODEC_NIL code (0.2.2.5), NOT the misleading
// DUPLICATE_REGISTRATION reason/code it conflated before the fix. Pre-fix the
// message read "[131585 DUPLICATE_REGISTRATION]"; this asserts the dotted-quad
// CodeCodecNil and the CODEC_NIL reason, and the absence of the wrong reason.
func TestRegister_NilUsesCodecNilCode(t *testing.T) {
	//: clean slate so an unrelated leftover cannot satisfy the assertion.
	codec.ResetForTest()
	type tc struct {
		name      string
		substr    string
		wantFound bool
	}
	tests := []tc{
		//: the dedicated dotted-quad code must appear (0.2.2.5 via Code.String()).
		{"dotted-quad CodeCodecNil present", codec.CodeCodecNil.String(), true},
		//: the dedicated reason must appear so operator triage is not misled.
		{"CODEC_NIL reason present", "CODEC_NIL", true},
		//: the OLD wrong reason must be gone — proves the conflation was removed.
		{"DUPLICATE_REGISTRATION reason absent", "DUPLICATE_REGISTRATION", false},
	}
	msg := recoverPanicMessage(t, func() { codec.Register(nil) })
	//: missing panic is an outright failure — Register(nil) must always abort.
	if msg == "" {
		t.Fatal("V18: Register(nil) did not panic")
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: assert presence or absence of the substring per the row's intent.
		if got := strings.Contains(msg, c.substr); got != c.wantFound {
			t.Errorf("V18 %s: Contains(%q)=%v, want %v (msg=%q)", c.name, c.substr, got, c.wantFound, msg)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestRegister_PanicsRenderDottedQuad is the V16 regression: every Register
// boot-failure message must render the Code as its canonical dotted-quad
// (Code.String()), never the raw decimal an integer-underlying %d emits.
// Pre-fix the duplicate-Name message read "[131585 DUPLICATE_REGISTRATION]";
// this asserts the dotted form is present and the decimal form is absent so
// the CLAUDE.md rule-4 log-parser regex matches.
func TestRegister_PanicsRenderDottedQuad(t *testing.T) {
	type tc struct {
		name    string
		run     func()
		wantDot string
		notDec  string
	}
	tests := []tc{
		{
			//: nil path → CodeCodecNil rendered dotted, never its decimal.
			name:    "nil panic renders dotted-quad",
			run:     func() { codec.Register(nil) },
			wantDot: codec.CodeCodecNil.String(),
			notDec:  strconv.FormatUint(uint64(codec.CodeCodecNil), 10),
		},
		{
			//: duplicate Name path → CodeDuplicateRegistration dotted, not decimal.
			name: "duplicate Name panic renders dotted-quad",
			run: func() {
				dup := &mockCodec{name: "v16-dup", mime: []string{"m/v16"}, ext: []string{".v16"}}
				codec.Register(dup)
				codec.Register(dup)
			},
			wantDot: codec.CodeDuplicateRegistration.String(),
			notDec:  strconv.FormatUint(uint64(codec.CodeDuplicateRegistration), 10),
		},
		{
			//: alias-conflict path → same dotted-quad rendering requirement.
			name: "alias conflict panic renders dotted-quad",
			run: func() {
				a := &mockCodec{name: "v16-a", mime: []string{"application/x-v16"}}
				b := &mockCodec{name: "v16-b", mime: []string{"application/x-v16"}}
				codec.Register(a)
				codec.Register(b)
			},
			wantDot: codec.CodeDuplicateRegistration.String(),
			notDec:  strconv.FormatUint(uint64(codec.CodeDuplicateRegistration), 10),
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: each case mutates the process-wide registry — reset for isolation.
		codec.ResetForTest()
		msg := recoverPanicMessage(t, c.run)
		//: a missing panic fails the case — the boot guard must fire.
		if msg == "" {
			t.Fatalf("%s: expected panic, got none", c.name)
		}
		//: the dotted-quad form must be present (Code.String()).
		if !strings.Contains(msg, c.wantDot) {
			t.Errorf("%s: panic %q missing dotted-quad %q", c.name, msg, c.wantDot)
		}
		//: the raw decimal must be ABSENT — its presence is the V16 bug.
		if strings.Contains(msg, c.notDec) {
			t.Errorf("%s: panic %q still renders raw decimal %q (want dotted-quad)", c.name, msg, c.notDec)
		}
	}
	for _, c := range tests {
		//: sequential — Register mutates the process-wide registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
