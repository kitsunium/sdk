// Package protobuf — white-box tests for the codec methods. The concrete type
// is unexported, so its methods are only reachable from inside the package.
package protobuf

import (
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// message builds a structpb.Struct fixture; structpb needs no codegen.
func message(t *testing.T) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(map[string]any{"id": "kitsunium"})
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	return s
}

// Test_protobufCodec_Name pins the canonical Format identifier. It is the
// registry key, so a drift here silently unregisters the codec from every
// consumer that asks for it by name.
func Test_protobufCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"the canonical identifier", "protobuf"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := (&protobufCodec{}).Name(); got != c.want {
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

// Test_protobufCodec_MIMETypes pins that the caller gets a copy. The list is a
// package-level table, so handing it out directly would let one consumer's
// append reach every other consumer — and content negotiation would start
// answering for a format nobody registered.
func Test_protobufCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		mutate func(got []string)
	}
	tests := []tc{
		{"the list as handed out", func([]string) {}},
		{"after the caller overwrites an entry", func(g []string) { g[0] = "text/plain" }},
		{"after the caller clears it", func(g []string) { clear(g) }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		want := slices.Clone(mimeTypes)
		got := (&protobufCodec{}).MIMETypes()

		c.mutate(got)

		//: the package table must be exactly as it was.
		if !slices.Equal(mimeTypes, want) {
			t.Errorf("the caller's mutation reached the package table: %v, want %v", mimeTypes, want)
		}
		//: and a later caller must still get the original list.
		if !slices.Equal((&protobufCodec{}).MIMETypes(), want) {
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

// Test_protobufCodec_Extensions pins the same defensive copy on the extension
// list.
func Test_protobufCodec_Extensions(t *testing.T) {
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
		got := (&protobufCodec{}).Extensions()

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

// Test_protobufCodec_Marshal pins that a value which is not a generated message
// is refused with the typed sentinel rather than reaching the library, which
// would report something the caller cannot classify.
func Test_protobufCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		build   func(t *testing.T) any
		wantErr bool
	}
	tests := []tc{
		{
			name:  "a generated message",
			build: func(t *testing.T) any { t.Helper(); return message(t) },
		},
		{name: "a scalar", build: func(*testing.T) any { return 42 }, wantErr: true},
		{name: "a plain struct", build: func(*testing.T) any { return struct{ A int }{1} }, wantErr: true},
		{name: "a map", build: func(*testing.T) any { return map[string]string{} }, wantErr: true},
		{name: "a nil value", build: func(*testing.T) any { return nil }, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := (&protobufCodec{}).Marshal(c.build(t))
		if c.wantErr {
			if !errs.HasCode(err, CodeProtobufMarshalFailed) {
				t.Fatalf("Marshal = %v, want PROTOBUF_MARSHAL_FAILED", err)
			}
			//: a refused encode must produce no partial output.
			if got != nil {
				t.Errorf("Marshal returned %q beside the error", got)
			}
			return
		}
		if err != nil {
			t.Fatalf("Marshal = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_protobufCodec_Unmarshal pins the size cap and the non-message target.
// The cap is a CWE-400 defence and has to fire BEFORE the decoder allocates,
// which is why it is asserted on a buffer that is only oversized, not valid
// wire data.
func Test_protobufCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		data     []byte
		target   func() any
		wantCode errs.Code
	}
	valid := func(t *testing.T) []byte {
		t.Helper()
		out, err := (&protobufCodec{}).Marshal(message(t))
		if err != nil {
			t.Fatalf("building the fixture: %v", err)
		}
		return out
	}
	tests := []tc{
		{
			name:   "valid wire bytes into a message",
			target: func() any { return &structpb.Struct{} },
		},
		{
			name:     "a target that is not a message",
			target:   func() any { return &struct{ A int }{} },
			wantCode: CodeProtobufUnmarshalFailed,
		},
		{
			name:     "a nil target",
			target:   func() any { return nil },
			wantCode: CodeProtobufUnmarshalFailed,
		},
		{
			//: one byte past the cap, so the refusal cannot be attributed to
			//: the decoder choking on the content.
			name:     "one byte over the size cap",
			data:     make([]byte, maxProtobufBytes+1),
			target:   func() any { return &structpb.Struct{} },
			wantCode: CodeProtobufSizeExceeded,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		data := c.data
		//: an unset case decodes the fixture the codec itself produced.
		if data == nil {
			data = valid(t)
		}

		err := (&protobufCodec{}).Unmarshal(data, c.target())
		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("Unmarshal = %v, want code %v", err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("Unmarshal = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_protobufCodec_Append pins the buffer contract: on success the encoding
// is appended, and on failure the caller's buffer comes back exactly as it went
// in. A codec that truncated a shared buffer on a bad value would corrupt data
// that had nothing to do with the failure.
func Test_protobufCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		dst     []byte
		build   func(t *testing.T) any
		wantErr bool
	}
	tests := []tc{
		{
			name:  "onto an empty buffer",
			build: func(t *testing.T) any { t.Helper(); return message(t) },
		},
		{
			name:  "onto a buffer with content",
			dst:   []byte("prefix"),
			build: func(t *testing.T) any { t.Helper(); return message(t) },
		},
		{
			name:    "a scalar leaves the buffer intact",
			dst:     []byte("prefix"),
			build:   func(*testing.T) any { return 42 },
			wantErr: true,
		},
		{
			name:    "a nil value leaves the buffer intact",
			dst:     []byte("prefix"),
			build:   func(*testing.T) any { return nil },
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		before := string(c.dst)

		got, err := (&protobufCodec{}).Append(c.dst, c.build(t))
		appended := string(got)

		if c.wantErr {
			if !errs.HasCode(err, CodeProtobufMarshalFailed) {
				t.Fatalf("Append = %v, want PROTOBUF_MARSHAL_FAILED", err)
			}
			//: the caller's bytes must be exactly as they were.
			if appended != before {
				t.Errorf("Append left the buffer as %q, want %q", appended, before)
			}
			return
		}
		if err != nil {
			t.Fatalf("Append = %v, want nil", err)
		}
		//: the prefix survives and the encoding follows it.
		if !strings.HasPrefix(appended, before) || appended == before {
			t.Errorf("Append = %q, want %q followed by the encoding", appended, before)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
