// Package bson — white-box tests for the codec methods. The concrete type is
// unexported, so its methods are only reachable from inside the package.
package bson

import (
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// document is a BSON document fixture.
type document struct {
	Name    string `bson:"name"`
	Replica int    `bson:"replica"`
}

// Test_bsonCodec_Name pins the canonical Format identifier. It is the registry
// key, so a drift here silently unregisters the codec from every consumer that
// asks for it by name.
func Test_bsonCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"the canonical identifier", "bson"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := (&bsonCodec{}).Name(); got != c.want {
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

// Test_bsonCodec_MIMETypes pins that the caller gets a copy. The list is a
// package-level table, so handing it out directly would let one consumer's
// append reach every other consumer — and content negotiation would start
// answering for a format nobody registered.
func Test_bsonCodec_MIMETypes(t *testing.T) {
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
		got := (&bsonCodec{}).MIMETypes()

		c.mutate(got)

		//: the package table must be exactly as it was.
		if !slices.Equal(mimeTypes, want) {
			t.Errorf("the caller's mutation reached the package table: %v, want %v", mimeTypes, want)
		}
		//: and a later caller must still get the original list.
		if !slices.Equal((&bsonCodec{}).MIMETypes(), want) {
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

// Test_bsonCodec_Extensions pins the same defensive copy on the extension list.
func Test_bsonCodec_Extensions(t *testing.T) {
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
		got := (&bsonCodec{}).Extensions()

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

// Test_bsonCodec_Marshal pins that a library failure comes back under the
// domain's own code. BSON documents are maps at the wire level, so a top-level
// scalar has nowhere to go — and the library error alone would leave the caller
// unable to tell it apart from a decode fault.
func Test_bsonCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr bool
	}
	tests := []tc{
		{name: "a tagged struct", in: document{Name: "kitsunium", Replica: 3}},
		{name: "a pointer to a tagged struct", in: &document{Name: "kitsunium"}},
		{name: "the zero struct", in: document{}},
		{name: "a map", in: map[string]any{"name": "kitsunium"}},
		{name: "a scalar", in: 42, wantErr: true},
		{name: "a string", in: "kitsunium", wantErr: true},
		{name: "a nil value", in: nil, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := (&bsonCodec{}).Marshal(c.in)
		if c.wantErr {
			if !errs.HasCode(err, CodeBSONMarshalFailed) {
				t.Fatalf("Marshal(%T) = %v, want BSON_MARSHAL_FAILED", c.in, err)
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
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_bsonCodec_Unmarshal pins the size cap and the library wrapping. The cap
// is the primary memory-exhaustion defence (CWE-400): a BSON document declares
// its own length, so the decoder pre-allocates from a number the attacker
// controls. It has to fire BEFORE the decoder runs, which is why it is asserted
// on a buffer that is only oversized, not valid BSON.
func Test_bsonCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		data     []byte
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a valid document"},
		{name: "truncated bytes", data: []byte{0x05}, wantCode: CodeBSONUnmarshalFailed},
		{name: "junk", data: []byte("definitely not bson"), wantCode: CodeBSONUnmarshalFailed},
		{
			//: one byte past the cap, so the refusal cannot be attributed to
			//: the decoder choking on the content.
			name:     "one byte over the size cap",
			data:     make([]byte, maxBSONBytes+1),
			wantCode: CodeBSONSizeExceeded,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		data := c.data
		//: an unset case decodes the document the codec itself produced.
		if data == nil {
			encoded, err := (&bsonCodec{}).Marshal(document{Name: "kitsunium", Replica: 3})
			if err != nil {
				t.Fatalf("building the fixture: %v", err)
			}
			data = encoded
		}

		var got document
		err := (&bsonCodec{}).Unmarshal(data, &got)
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

// Test_bsonCodec_Append pins the buffer contract: on success the encoding is
// appended, and on failure the caller's buffer comes back exactly as it went
// in. A codec that truncated a shared buffer on a bad value would corrupt data
// that had nothing to do with the failure.
func Test_bsonCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		dst     []byte
		in      any
		wantErr bool
	}
	tests := []tc{
		{name: "onto an empty buffer", in: document{Name: "kitsunium", Replica: 1}},
		{name: "onto a buffer with content", dst: []byte("prefix"), in: document{Name: "a"}},
		{name: "a scalar leaves the buffer intact", dst: []byte("prefix"), in: 42, wantErr: true},
		{name: "a nil value leaves the buffer intact", dst: []byte("prefix"), in: nil, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		before := string(c.dst)

		got, err := (&bsonCodec{}).Append(c.dst, c.in)
		appended := string(got)

		if c.wantErr {
			if !errs.HasCode(err, CodeBSONMarshalFailed) {
				t.Fatalf("Append(%T) = %v, want BSON_MARSHAL_FAILED", c.in, err)
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
