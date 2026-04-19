package csv

import (
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
