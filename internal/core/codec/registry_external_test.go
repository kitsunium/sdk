package codec_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
)

// mockCodec is a minimal Codec impl used only by the registry tests.
// KTN-INTERFACE-ANYUSE is excluded for this tree in .ktn-linter.yaml.
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
