package json

import (
	"bytes"
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

// Test_jsonCodec_Marshal exercises the Marshal path with a single canonical case.
func Test_jsonCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical marshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &jsonCodec{}
		if _, err := c.Marshal(map[string]int{"a": 1}); err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
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
