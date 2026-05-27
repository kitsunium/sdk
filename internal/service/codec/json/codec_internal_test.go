package json

import (
	"bytes"
	stdjson "encoding/json"
	"testing"
)

// Test_jsonCodec_Name covers the canonical identifier returned by the codec.
func Test_jsonCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"canonical identifier", "json"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &jsonCodec{}
		if got := c.Name(); got != tc.want {
			t.Errorf("%s: Name=%q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_jsonCodec_MIMETypes covers the MIME list and its copy semantics.
func Test_jsonCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical MIME first", "application/json"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &jsonCodec{}
		mimes := c.MIMETypes()
		if len(mimes) == 0 || mimes[0] != tc.wantHead {
			t.Errorf("%s: MIMETypes=%v, want head %q", tc.name, mimes, tc.wantHead)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_jsonCodec_Extensions covers the extension list.
func Test_jsonCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical extension first", ".json"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &jsonCodec{}
		exts := c.Extensions()
		if len(exts) == 0 || exts[0] != tc.wantHead {
			t.Errorf("%s: Extensions=%v, want head %q", tc.name, exts, tc.wantHead)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_jsonCodec_Marshal exercises the Marshal path: the reflect route,
// the RawMessage pre-encoded fast-path, and the wrapped-error branch.
func Test_jsonCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		want    string
		wantErr bool
	}
	tests := []tc{
		//: reflect route — map encodes via stdjson.Marshal.
		{"reflect-route", map[string]int{"a": 1}, `{"a":1}`, false},
		//: RawMessage fast-path — Marshal returns the pre-encoded bytes
		//: verbatim (lines 55-58) instead of the reflect round-trip.
		{"raw-message-fast-path", stdjson.RawMessage(`{"pre":true}`), `{"pre":true}`, false},
		//: error route — a channel cannot be JSON-encoded, so stdjson.Marshal
		//: fails and Marshal wraps it (lines 67-72).
		{"unsupported-value-wraps-error", make(chan int), "", true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &jsonCodec{}
		out, err := c.Marshal(tc.in)
		//: error branch — verify failure surfaced, value bytes irrelevant.
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected error, got nil", tc.name)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		//: success branch — assert exact wire bytes for both routes.
		if string(out) != tc.want {
			t.Errorf("%s: Marshal=%q want %q", tc.name, out, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_jsonCodec_Unmarshal exercises the Unmarshal path.
func Test_jsonCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical unmarshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &jsonCodec{}
		var out map[string]int
		if err := c.Unmarshal([]byte("{"), &out); err == nil {
			t.Errorf("%s: expected Unmarshal error", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_jsonCodec_NewEncoder covers the streaming encoder constructor.
func Test_jsonCodec_NewEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil encoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &jsonCodec{}
		if enc := c.NewEncoder(&bytes.Buffer{}); enc == nil {
			t.Errorf("%s: NewEncoder returned nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_jsonCodec_NewDecoder covers the streaming decoder constructor.
func Test_jsonCodec_NewDecoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil decoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &jsonCodec{}
		if dec := c.NewDecoder(bytes.NewReader(nil)); dec == nil {
			t.Errorf("%s: NewDecoder returned nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

func Test_jsonCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		dst       []byte
		v         any
		wantBytes []byte
		wantErr   bool
	}
	tests := []tc{
		{"appends to empty buffer", nil, map[string]int{"k": 1}, []byte(`{"k":1}`), false},
		{"appends to non-empty buffer", []byte("prefix:"), 7, []byte("prefix:7"), false},
		//: RawMessage fast-path — Append writes the pre-encoded bytes onto
		//: dst directly (lines 139-142), skipping the scratch encoder.
		{"raw-message-fast-path", []byte("pre:"), stdjson.RawMessage(`{"x":1}`), []byte(`pre:{"x":1}`), false},
		{"unsupported value yields error", nil, make(chan int), nil, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &jsonCodec{}
		got, err := c.Append(tc.dst, tc.v)
		if (err != nil) != tc.wantErr {
			t.Errorf("Append err = %v, wantErr = %v", err, tc.wantErr)
		}
		if tc.wantErr {
			return
		}
		if !bytes.Equal(got, tc.wantBytes) {
			t.Errorf("Append bytes = %q, want %q", got, tc.wantBytes)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_marshalRawMessage pins the json.RawMessage / *json.RawMessage
// pass-through fast path: pre-encoded inputs return their bytes
// verbatim (defensive-cloned) instead of going through stdjson's
// reflect + MarshalJSON round-trip.
func Test_marshalRawMessage(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		input  any
		wantOK bool
		want   string
	}
	raw := new(stdjson.RawMessage(`{"k":"v"}`))
	tests := []tc{
		{"raw-message-non-empty", stdjson.RawMessage(`{"a":1}`), true, `{"a":1}`},
		{"raw-message-empty-as-null", stdjson.RawMessage{}, true, `null`},
		{"raw-message-pointer", raw, true, `{"k":"v"}`},
		{"nil-pointer-as-null", (*stdjson.RawMessage)(nil), true, `null`},
		{"non-raw-falls-back", "plain-string", false, ""},
		{"struct-falls-back", struct{ X int }{1}, false, ""},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, ok := marshalRawMessage(tc.input)
		if ok != tc.wantOK {
			t.Errorf("%s: ok=%v want=%v", tc.name, ok, tc.wantOK)
		}
		if tc.wantOK && string(out) != tc.want {
			t.Errorf("%s: got=%q want=%q", tc.name, out, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
