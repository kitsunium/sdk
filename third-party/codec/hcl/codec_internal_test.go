// Package hcl — white-box tests for the codec methods. The concrete type is
// unexported, so its methods are only reachable from inside the package.
package hcl

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// doc is a tagged struct fixture; gohcl encodes only from struct fields.
type doc struct {
	Name    string `hcl:"name"`
	Replica int    `hcl:"replica"`
}

// Test_hclCodec_Name pins the canonical Format identifier. It is the registry
// key, so a drift here silently unregisters the codec from every consumer that
// asks for it by name.
func Test_hclCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"the canonical identifier", "hcl"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := (&hclCodec{}).Name(); got != c.want {
			t.Errorf("Name() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_hclCodec_MIMETypes pins that the caller gets a copy. The list is a
// package-level table, so handing it out directly would let one consumer's
// append reach every other consumer — and content negotiation would start
// answering for a format nobody registered.
func Test_hclCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		mutate  func(got []string)
		wantLen int
	}
	tests := []tc{
		{"the list as handed out", func([]string) {}, 1},
		{"after the caller overwrites an entry", func(g []string) { g[0] = "text/plain" }, 1},
		{"after the caller clears it", func(g []string) { clear(g) }, 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		want := slices.Clone(mimeTypes)
		got := (&hclCodec{}).MIMETypes()
		if len(got) != c.wantLen {
			t.Fatalf("MIMETypes() = %v, want %d entries", got, c.wantLen)
		}

		c.mutate(got)

		//: the package table must be exactly as it was.
		if !slices.Equal(mimeTypes, want) {
			t.Errorf("the caller's mutation reached the package table: %v, want %v", mimeTypes, want)
		}
		//: and a second caller must still get the original list.
		if !slices.Equal((&hclCodec{}).MIMETypes(), want) {
			t.Error("a later caller saw the first caller's mutation")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_hclCodec_Extensions pins the same defensive copy on the extension list.
func Test_hclCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		mutate func(got []string)
	}
	tests := []tc{
		{"the list as handed out", func([]string) {}},
		{"after the caller overwrites an entry", func(g []string) { g[0] = ".txt" }},
		{"after the caller clears it", func(g []string) { clear(g) }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		want := slices.Clone(extensions)
		got := (&hclCodec{}).Extensions()

		c.mutate(got)

		if !slices.Equal(extensions, want) {
			t.Errorf("the caller's mutation reached the package table: %v, want %v", extensions, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_structLike pins the guard that keeps gohcl from panicking. Everything
// this rejects would otherwise reach EncodeIntoBody, which panics rather than
// returning an error — and a panic inside a codec takes the caller's process
// with it.
func Test_structLike(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
		want bool
	}
	tests := []tc{
		{"a struct", doc{}, true},
		{"a pointer to a struct", &doc{}, true},
		{"an integer", 42, false},
		{"a string", "hcl", false},
		{"a slice", []doc{}, false},
		{"a map", map[string]string{}, false},
		{"a nil interface value", nil, false},
		//: a pointer to a pointer is one level too deep for gohcl.
		{"a pointer to a pointer to a struct", new(*doc), false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := structLike(reflect.TypeOf(c.in)); got != c.want {
			t.Errorf("structLike(%T) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_hclCodec_Marshal pins that every rejection is a typed error rather than
// a panic escaping the codec.
func Test_hclCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr bool
	}
	tests := []tc{
		{name: "a tagged struct", in: doc{Name: "kitsunium", Replica: 3}},
		{name: "a pointer to a tagged struct", in: &doc{Name: "kitsunium"}},
		{name: "the zero struct", in: doc{}},
		{name: "a scalar", in: 42, wantErr: true},
		{name: "a slice", in: []doc{{Name: "a"}}, wantErr: true},
		{name: "a nil value", in: nil, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := (&hclCodec{}).Marshal(c.in)
		if c.wantErr {
			if !errs.HasCode(err, CodeHCLMarshalFailed) {
				t.Fatalf("Marshal(%T) = %v, want HCL_MARSHAL_FAILED", c.in, err)
			}
			//: a refused encode must produce no partial output.
			if got != nil {
				t.Errorf("Marshal(%T) returned %q beside the error", c.in, got)
			}
			return
		}
		if err != nil {
			t.Fatalf("Marshal(%T) = %v, want nil", c.in, err)
		}
		if len(got) == 0 {
			t.Errorf("Marshal(%T) produced nothing", c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_hclCodec_Unmarshal pins the size cap and the diagnostic wrapping. The
// cap is a CWE-400 defence and has to fire BEFORE the parser allocates, which
// is why it is asserted on a buffer that is only oversized, not valid HCL.
func Test_hclCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		data     []byte
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a valid document", data: []byte("name = \"kitsunium\"\nreplica = 3\n")},
		{
			name:     "a syntax error",
			data:     []byte("name = \n"),
			wantCode: CodeHCLUnmarshalFailed,
		},
		{
			name:     "a field the schema does not declare",
			data:     []byte("name = \"a\"\nreplica = 1\nunknown = true\n"),
			wantCode: CodeHCLUnmarshalFailed,
		},
		{
			//: one byte past the cap, so the refusal cannot be attributed to
			//: the parser choking on the content.
			name:     "one byte over the size cap",
			data:     make([]byte, maxHCLBytes+1),
			wantCode: CodeHCLSizeExceeded,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got doc
		err := (&hclCodec{}).Unmarshal(c.data, &got)
		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("Unmarshal = %v, want code %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("Unmarshal = %v, want nil", err)
		}
		if got.Name != "kitsunium" || got.Replica != 3 {
			t.Errorf("Unmarshal decoded %+v", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_hclCodec_Append pins the buffer contract: on success the encoding is
// appended, and on failure the caller's buffer comes back exactly as it went
// in. A codec that truncated a shared buffer on a bad value would corrupt data
// that had nothing to do with the failure.
func Test_hclCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		dst     []byte
		in      any
		wantErr bool
	}
	tests := []tc{
		{name: "onto an empty buffer", in: doc{Name: "kitsunium", Replica: 1}},
		{name: "onto a buffer with content", dst: []byte("prefix\n"), in: doc{Name: "a", Replica: 1}},
		{name: "a scalar leaves the buffer intact", dst: []byte("prefix\n"), in: 42, wantErr: true},
		{name: "a nil value leaves the buffer intact", dst: []byte("prefix\n"), in: nil, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		before := string(c.dst)

		got, err := (&hclCodec{}).Append(c.dst, c.in)
		appended := string(got)

		if c.wantErr {
			if !errs.HasCode(err, CodeHCLMarshalFailed) {
				t.Fatalf("Append(%T) = %v, want HCL_MARSHAL_FAILED", c.in, err)
			}
			//: the caller's bytes must be exactly as they were.
			if appended != before {
				t.Errorf("Append(%T) left the buffer as %q, want %q", c.in, appended, before)
			}
			return
		}
		if err != nil {
			t.Fatalf("Append(%T) = %v, want nil", c.in, err)
		}
		//: the prefix survives and the encoding follows it.
		if !strings.HasPrefix(appended, before) || appended == before {
			t.Errorf("Append(%T) = %q, want %q followed by the encoding", c.in, appended, before)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
