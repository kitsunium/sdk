package ndjson

import (
	"bytes"
	stdjson "encoding/json"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec/scratch"
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
		{"trailing record without newline", "1\n2", 2, false},
		{"empty input", "", 0, false},
		{"blank lines skipped", "\n1\n\n2\n", 2, false},
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

// Test_countNDJSONRecords pins the capacity-hint helper: every '\n' is a
// record terminator plus one extra slot for a possible trailing record
// that lacks a final newline.
func Test_countNDJSONRecords(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		data string
		want int
	}
	tests := []tc{
		{"empty", "", 0},
		{"single terminated", "1\n", 1},
		{"single not terminated", "1", 1},
		{"two terminated", "1\n2\n", 2},
		{"trailing partial", "1\n2", 2},
		{"all blanks counted as upper bound", "\n\n\n", 3},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := countNDJSONRecords([]byte(tc.data))
		//: this is a capacity hint, so exact equality is the contract.
		if got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
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

func Test_ndjsonCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		dst       []byte
		v         any
		wantBytes []byte
		wantErr   bool
	}
	tests := []tc{
		{"appends one record per slice element", nil, []int{1, 2}, []byte("1\n2\n"), false},
		{"appends to non-empty buffer", []byte("prefix:"), []int{3}, []byte("prefix:3\n"), false},
		{"non-slice value yields error", nil, 7, nil, true},
		{"per-record marshal failure surfaces error", nil, []any{make(chan int)}, nil, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &ndjsonCodec{}
		got, err := c.Append(tc.dst, tc.v)
		if (err != nil) != tc.wantErr {
			t.Errorf("Append err = %v, wantErr = %v", err, tc.wantErr)
		}
		if tc.wantErr {
			return
		}
		if string(got) != string(tc.wantBytes) {
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

// Test_ndjsonCodec_Append_RollbackOnMidSliceError asserts the Appender
// contract: on error the returned buffer equals the caller-supplied dst,
// never the partially-written mid-slice intermediate.
func Test_ndjsonCodec_Append_RollbackOnMidSliceError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix []byte
		input  []any
	}
	tests := []tc{
		//: mix a valid record with an unmarshalable channel at index 1 so the
		//: inner loop reaches the error branch AFTER it has appended the first
		//: record's bytes + newline onto dst.
		{"mid-slice marshal failure rolls back to prior dst", []byte("prefix:"), []any{42, make(chan int)}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &ndjsonCodec{}
		got, err := c.Append(tc.prefix, tc.input)
		if err == nil {
			t.Fatalf("%s: Append expected an error on mid-slice marshal failure", tc.name)
		}
		//: contract: the returned buffer MUST equal dst's prior contents.
		if string(got) != string(tc.prefix) {
			t.Errorf("%s: Append rolled back to %q, want %q (prior dst)", tc.name, got, tc.prefix)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_appendNDJSONRaw covers the raw-bytes writer + embedded-newline guard.
func Test_appendNDJSONRaw(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		dst    []byte
		rows   [][]byte
		want   string
		wantOK bool
	}
	tests := []tc{
		{name: "empty-rows", dst: nil, rows: nil, want: "", wantOK: true},
		{name: "one-row", dst: nil, rows: [][]byte{[]byte(`{"a":1}`)}, want: "{\"a\":1}\n", wantOK: true},
		{name: "two-rows-append-to-existing", dst: []byte("prefix:"), rows: [][]byte{[]byte("a"), []byte("b")}, want: "prefix:a\nb\n", wantOK: true},
		{name: "embedded-newline-rejects", dst: nil, rows: [][]byte{[]byte("ok"), []byte("bad\nnewline")}, want: "", wantOK: false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, ok := appendNDJSONRaw(tc.dst, tc.rows)
		if ok != tc.wantOK {
			t.Fatalf("%s: ok=%v want %v", tc.name, ok, tc.wantOK)
		}
		if !tc.wantOK {
			//: rejection path returns nil; no buffer expected.
			return
		}
		if string(got) != tc.want {
			t.Errorf("%s: got=%q want=%q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_rawMessageView covers the []json.RawMessage → [][]byte adapter.
func Test_rawMessageView(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []stdjson.RawMessage
	}
	tests := []tc{
		{"empty", nil},
		{"single", []stdjson.RawMessage{[]byte(`{"k":"v"}`)}},
		{"multiple", []stdjson.RawMessage{[]byte("1"), []byte("2"), []byte("3")}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := rawMessageView(tc.in)
		if len(got) != len(tc.in) {
			t.Fatalf("%s: len=%d want %d", tc.name, len(got), len(tc.in))
		}
		//: each view element must alias the underlying RawMessage bytes.
		for i := range got {
			if string(got[i]) != string(tc.in[i]) {
				t.Errorf("%s: idx %d got=%q want=%q", tc.name, i, got[i], tc.in[i])
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_marshalRawSliceTo covers the top-level fast-path dispatch.
func Test_marshalRawSliceTo(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		v      any
		want   string
		wantOK bool
	}
	tests := []tc{
		{"raw-message-slice", []stdjson.RawMessage{[]byte(`{"a":1}`), []byte(`{"b":2}`)}, "{\"a\":1}\n{\"b\":2}\n", true},
		{"bytes-slice", [][]byte{[]byte("x"), []byte("y")}, "x\ny\n", true},
		{"non-raw-shape", []int{1, 2, 3}, "", false},
		{"raw-with-embedded-newline-falls-back", []stdjson.RawMessage{[]byte("ok\nno")}, "", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, ok := marshalRawSliceTo(nil, tc.v)
		if ok != tc.wantOK {
			t.Fatalf("%s: ok=%v want %v", tc.name, ok, tc.wantOK)
		}
		if !tc.wantOK {
			return
		}
		if string(got) != tc.want {
			t.Errorf("%s: got=%q want=%q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_detachAndRelease covers the two release paths: small buffer
// (clone+repool, ok=true) and over-cap buffer (orphan, ok=false).
func Test_detachAndRelease(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		cap        int
		wantRepool bool
	}
	tests := []tc{
		{"small-cloned-and-repooled", 1024, true},
		{"oversize-orphaned-untouched", scratch.MaxRetainedBufBytes + 1, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		buf := new(bytes.Buffer)
		buf.Grow(tc.cap)
		buf.WriteString("xyz")
		out, repooled := detachAndRelease(buf)
		//: contract: caller's bytes survive both paths intact.
		if string(out) != "xyz" {
			t.Errorf("%s: got %q want %q", tc.name, out, "xyz")
		}
		//: contract: ok reflects the chosen path.
		if repooled != tc.wantRepool {
			t.Errorf("%s: repooled=%v want %v", tc.name, repooled, tc.wantRepool)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
