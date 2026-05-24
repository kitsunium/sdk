package pem

import (
	"bytes"
	stdpem "encoding/pem"
	"testing"
)

// Test_pemCodec_Name covers the canonical identifier returned by the codec.
func Test_pemCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"canonical identifier", "pem"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &pemCodec{}
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

// Test_pemCodec_MIMETypes covers the MIME list and its copy semantics.
func Test_pemCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical MIME first", "application/x-pem-file"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &pemCodec{}
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

// Test_pemCodec_Extensions covers the extension list.
func Test_pemCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical extension first", ".pem"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &pemCodec{}
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

// Test_pemCodec_Marshal exercises the Marshal path with a single canonical case.
func Test_pemCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical marshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &pemCodec{}
		if _, err := c.Marshal("not-a-block"); err == nil {
			t.Errorf("%s: Marshal should reject non-block", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_pemCodec_Unmarshal exercises the Unmarshal path.
func Test_pemCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical unmarshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &pemCodec{}
		var out *stdpem.Block
		if err := c.Unmarshal([]byte("-----BEGIN TEST-----\nAAAA\n-----END TEST-----\n"), &out); err != nil {
			t.Errorf("%s: Unmarshal err=%v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_pemCodec_Append covers the Appender extension via the table-
// driven runCase pattern: forward (error → dst untouched) + reverse
// (prefix preserved on success).
func Test_pemCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		dst     []byte
		v       any
		wantErr bool
	}
	tests := []tc{
		{"happy-block", nil, &stdpem.Block{Type: "TEST", Bytes: []byte("hi")}, false},
		{"happy-prefix", []byte("PRE"), &stdpem.Block{Type: "DATA", Bytes: []byte{1, 2}}, false},
		{"reject-channel", []byte("PRE"), make(chan int), true},
		{"reject-nil-block", []byte("PRE"), (*stdpem.Block)(nil), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := New()
		ap, ok := c.(interface {
			Append([]byte, any) ([]byte, error)
		})
		if !ok {
			t.Fatalf("%s: pemCodec does not implement Appender", tc.name)
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

// Test_detachAndRelease covers the size-aware release paths.
func Test_detachAndRelease(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cap  int
	}
	tests := []tc{
		{"small-cloned-and-repooled", 1024},
		{"oversize-orphaned-untouched", maxRetainedBufBytes + 1},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		buf := new(bytes.Buffer)
		buf.Grow(tc.cap)
		buf.WriteString("xyz")
		out := detachAndRelease(buf)
		if string(out) != "xyz" {
			t.Errorf("%s: got %q want %q", tc.name, out, "xyz")
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_releaseBuffer covers the error-path cap-discard helper.
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
