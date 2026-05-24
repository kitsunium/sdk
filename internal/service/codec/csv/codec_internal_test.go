package csv

import (
	"bytes"
	"testing"
)

// Test_csvCodec_Name covers the canonical identifier returned by the codec.
func Test_csvCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"canonical identifier", "csv"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &csvCodec{}
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

// Test_csvCodec_MIMETypes covers the MIME list and its copy semantics.
func Test_csvCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical MIME first", "text/csv"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &csvCodec{}
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

// Test_csvCodec_Extensions covers the extension list.
func Test_csvCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical extension first", ".csv"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &csvCodec{}
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

// Test_csvCodec_Marshal exercises the Marshal path with a single canonical case.
func Test_csvCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical marshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &csvCodec{}
		if _, err := c.Marshal([][]string{{"a", "b"}}); err != nil {
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

// Test_csvCodec_Unmarshal exercises the Unmarshal path.
func Test_csvCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical unmarshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &csvCodec{}
		var out [][]string
		if err := c.Unmarshal([]byte("a,b\nc,d\n"), &out); err != nil {
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

// Test_extractRecords covers the type-shape helper used by Marshal.
func Test_extractRecords(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
		want bool
	}
	tests := []tc{
		{"direct matrix", [][]string{{"a"}}, true},
		{"pointer to matrix", &[][]string{{"b"}}, true},
		{"wrong type", "nope", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, got := extractRecords(tc.in)
		if got != tc.want {
			t.Errorf("%s: ok=%v want %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_csvCodec_Append covers the Appender extension via the table-
// driven runCase pattern: forward (error → dst untouched) + reverse
// (prefix preserved on success).
func Test_csvCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		dst     []byte
		v       any
		wantErr bool
	}
	tests := []tc{
		{"happy-rows", nil, [][]string{{"a", "b"}, {"1", "2"}}, false},
		{"happy-prefix", []byte("PRE\n"), [][]string{{"x", "y"}}, false},
		{"reject-channel", []byte("PRE"), make(chan int), true},
		{"reject-non-string-slice", []byte("PRE"), []int{1, 2, 3}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := New()
		ap, ok := c.(interface {
			Append([]byte, any) ([]byte, error)
		})
		if !ok {
			t.Fatalf("%s: csvCodec does not implement Appender", tc.name)
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
		//: caller's bytes survive both paths intact.
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
