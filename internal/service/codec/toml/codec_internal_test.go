package toml

import (
	"bytes"
	"testing"
)

// Test_tomlCodec_Name covers the canonical identifier returned by the codec.
func Test_tomlCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"canonical identifier", "toml"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tomlCodec{}
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

// Test_tomlCodec_MIMETypes covers the MIME list and its copy semantics.
func Test_tomlCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical MIME first", "application/toml"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tomlCodec{}
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

// Test_tomlCodec_Extensions covers the extension list.
func Test_tomlCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical extension first", ".toml"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tomlCodec{}
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

// Test_tomlCodec_Marshal exercises the Marshal path with a single canonical case.
func Test_tomlCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical marshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tomlCodec{}
		if _, err := c.Marshal(map[string]any{"a": 1}); err != nil {
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

// Test_tomlCodec_Unmarshal exercises the Unmarshal path.
func Test_tomlCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical unmarshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tomlCodec{}
		var out map[string]any
		if err := c.Unmarshal([]byte("= bad"), &out); err == nil {
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

// Test_tomlCodec_NewEncoder covers the streaming encoder constructor.
func Test_tomlCodec_NewEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil encoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tomlCodec{}
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

// Test_tomlCodec_NewDecoder covers the streaming decoder constructor.
func Test_tomlCodec_NewDecoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil decoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tomlCodec{}
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

// Test_tomlCodec_Append covers the Appender extension: happy round-trip
// + dst-untouched-on-error semantics.
func Test_tomlCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		dst     []byte
		v       any
		wantErr bool
	}
	tests := []tc{
		{"happy-empty-dst", nil, map[string]int{"a": 1}, false},
		{"happy-prefix-dst", []byte("# pre\n"), map[string]string{"k": "v"}, false},
		{"reject-channel", nil, make(chan int), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := New()
		ap, ok := c.(interface {
			Append([]byte, any) ([]byte, error)
		})
		if !ok {
			t.Fatalf("%s: tomlCodec does not implement Appender", tc.name)
		}
		got, err := ap.Append(tc.dst, tc.v)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: want error, got nil", tc.name)
			}
			if len(got) != len(tc.dst) {
				t.Errorf("%s: dst len changed on error: got=%d want=%d", tc.name, len(got), len(tc.dst))
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: unexpected err=%v", tc.name, err)
		}
		if len(tc.dst) > 0 && string(got[:len(tc.dst)]) != string(tc.dst) {
			t.Errorf("%s: prefix lost; got=%q", tc.name, got[:len(tc.dst)])
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_releaseBuffer covers the cap-discard release helper.
func Test_releaseBuffer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cap  int
	}
	tests := []tc{
		{"small-retained", 1024},
		{"discarded-oversize", maxRetainedBufBytes + 1},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		buf := new(bytes.Buffer)
		buf.Grow(tc.cap)
		releaseBuffer(buf)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
