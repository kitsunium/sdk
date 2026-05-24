package cbor

import (
	"bytes"
	"testing"
)

// Test_cborCodec_Name covers the canonical identifier returned by the codec.
func Test_cborCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"canonical identifier", "cbor"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &cborCodec{}
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

// Test_cborCodec_MIMETypes covers the MIME list and its copy semantics.
func Test_cborCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical MIME first", "application/cbor"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &cborCodec{}
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

// Test_cborCodec_Extensions covers the extension list.
func Test_cborCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical extension first", ".cbor"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &cborCodec{}
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

// Test_cborCodec_Marshal exercises the Marshal path with a single canonical case.
func Test_cborCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical marshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &cborCodec{}
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

// Test_cborCodec_Unmarshal exercises the Unmarshal path.
func Test_cborCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical unmarshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &cborCodec{}
		var out map[string]int
		if err := c.Unmarshal([]byte{0xff, 0x00}, &out); err == nil {
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

// Test_cborCodec_NewEncoder covers the streaming encoder constructor.
func Test_cborCodec_NewEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil encoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &cborCodec{}
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

// Test_mustHardenedDecMode covers the hardened DecMode constructor: the
// returned mode must be non-nil so every cborCodec.Unmarshal call has a
// usable decoder. The defensive panic branch is not exercised here
// because DecOptions.DecMode() does not fail for the caps we set.
func Test_mustHardenedDecMode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil hardened DecMode"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		mode := mustHardenedDecMode()
		if mode == nil {
			t.Errorf("%s: mustHardenedDecMode returned nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_mustEncMode covers the reusable EncMode constructor: the
// returned mode must be non-nil so every cborCodec.Marshal call hits
// the cached resolver rather than rebuilding default EncOptions per
// call. Defensive panic branch is unreachable for default options.
func Test_mustEncMode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil EncMode"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		mode := mustEncMode()
		if mode == nil {
			t.Errorf("%s: mustEncMode returned nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_cborCodec_NewDecoder covers the streaming decoder constructor.
func Test_cborCodec_NewDecoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil decoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &cborCodec{}
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

// Test_mustUserBufferEncMode covers the UserBufferEncMode constructor.
func Test_mustUserBufferEncMode(t *testing.T) {
	t.Parallel()
	mode := mustUserBufferEncMode()
	if mode == nil {
		t.Errorf("mustUserBufferEncMode returned nil")
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

// Test_cborCodec_Append covers the Appender extension on the CBOR codec.
func Test_cborCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		dst     []byte
		v       any
		wantErr bool
	}
	tests := []tc{
		{"happy-empty-dst", nil, map[string]int{"a": 1}, false},
		{"happy-prefix-dst", []byte("PRE"), "hello", false},
		{"reject-channel", nil, make(chan int), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := New()
		ap, ok := c.(interface {
			Append([]byte, any) ([]byte, error)
		})
		if !ok {
			t.Fatalf("%s: cborCodec does not implement Appender", tc.name)
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
			t.Errorf("%s: prefix lost", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
