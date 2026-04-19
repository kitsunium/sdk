package ndjson

import (
	"reflect"
	"testing"
)

// Test_ndjsonCodec_Name covers the canonical identifier returned by the codec.
func Test_ndjsonCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"canonical identifier", "ndjson"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &ndjsonCodec{}
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

// Test_ndjsonCodec_MIMETypes covers the MIME list and its copy semantics.
func Test_ndjsonCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical MIME first", "application/x-ndjson"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &ndjsonCodec{}
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

// Test_ndjsonCodec_Extensions covers the extension list.
func Test_ndjsonCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical extension first", ".ndjson"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &ndjsonCodec{}
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

// Test_ndjsonCodec_Marshal exercises the Marshal path with a single canonical case.
func Test_ndjsonCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical marshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &ndjsonCodec{}
		if _, err := c.Marshal([]int{1, 2}); err != nil {
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

// Test_ndjsonCodec_Unmarshal exercises the Unmarshal path.
func Test_ndjsonCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"canonical unmarshal"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &ndjsonCodec{}
		var out []int
		if err := c.Unmarshal([]byte("1\n2\n"), &out); err != nil {
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

// Test_asSlice covers the slice-detection helper behind Marshal.
func Test_asSlice(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
		want bool
	}
	tests := []tc{
		{"direct slice", []int{1}, true},
		{"pointer to slice", &[]int{1}, true},
		{"non-slice value", 42, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, got := asSlice(tc.in)
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

// Test_asSlicePointer covers the target-shape check behind Unmarshal.
func Test_asSlicePointer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   any
		want bool
	}
	tests := []tc{
		{"pointer to slice", &[]int{}, true},
		{"non-pointer", []int{}, false},
		{"pointer to non-slice", new(int), false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, got := asSlicePointer(tc.in)
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

// Test_decodeLines covers the line-scanner helper directly.
func Test_decodeLines(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    string
		wantLen int
		wantErr bool
	}
	tests := []tc{
		{"two valid records", "1\n2\n", 2, false},
		{"bad JSON surfaces error", "not json\n", 0, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var target []int
		typ := reflectTypeOf(&target).Elem()
		out, err := decodeLines([]byte(tc.data), typ)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
		if !tc.wantErr && out.Len() != tc.wantLen {
			t.Errorf("%s: decoded %d want %d", tc.name, out.Len(), tc.wantLen)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// reflectTypeOf returns reflect.TypeOf as a local alias to keep the helper
// import list obvious at the call site.
func reflectTypeOf(v any) reflect.Type { return reflect.TypeOf(v) }
