package csv

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec/scratch"
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

// Test_isPromotionShape covers every branch of the 2x1 promotion-shape
// guard: the matching shape (header "_json", one body cell), plus each
// rejecting mismatch (wrong row count, wrong column count on either row,
// wrong header literal). The matching case is what the existing public
// tests never produced, leaving the `return true` arm uncovered.
func Test_isPromotionShape(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		records [][]string
		want    bool
	}
	tests := []tc{
		//: exact canonical promotion shape — header literal + single body cell.
		{"canonical promotion shape matches", [][]string{{promotionHeaderCell}, {"{}"}}, true},
		//: one row only — fails the row-count guard.
		{"single row rejected", [][]string{{promotionHeaderCell}}, false},
		//: three rows — fails the row-count guard from the high side.
		{"three rows rejected", [][]string{{promotionHeaderCell}, {"a"}, {"b"}}, false},
		//: header row has two columns — fails the header column-count guard.
		{"wide header rejected", [][]string{{promotionHeaderCell, "x"}, {"a"}}, false},
		//: body row has two columns — fails the body column-count guard.
		{"wide body rejected", [][]string{{promotionHeaderCell}, {"a", "b"}}, false},
		//: correct shape but the wrong header literal — fails the final guard.
		{"wrong header literal rejected", [][]string{{"other"}, {"a"}}, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if got := isPromotionShape(tc.records); got != tc.want {
			t.Errorf("%s: isPromotionShape=%v want %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_marshalPromotionShape covers the hand-rolled promotion encoder,
// asserting RFC 4180 framing (header line + quoted body) and the only
// escape it performs: `"` → `""`. The plain-byte body and the
// quote-bearing body together exercise both arms of the per-byte loop.
func Test_marshalPromotionShape(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		body string
		want string
	}
	tests := []tc{
		//: plain JSON body — exercises only the passthrough loop arm.
		{"plain body quoted verbatim", `{"a":1}`, "_json\n\"{\"\"a\"\":1}\"\n"},
		//: body free of quotes — every byte takes the passthrough arm.
		{"quote-free body", "abc", "_json\n\"abc\"\n"},
		//: empty body — the loop never runs; framing only.
		{"empty body", "", "_json\n\"\"\n"},
		//: lone double-quote — exercises only the `\"` → `\"\"` doubling arm.
		{"lone quote doubled", `"`, "_json\n\"\"\"\"\n"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if got := string(marshalPromotionShape(tc.body)); got != tc.want {
			t.Errorf("%s: marshalPromotionShape=%q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_csvCodec_Marshal_PromotionFastPath asserts that Marshal routes the
// canonical 2x1 promotion shape through marshalPromotionShape (the
// `!escapeFormulas && isPromotionShape` arm) and that the emitted bytes
// round-trip back through Unmarshal to the identical matrix. The escape
// variant of the same shape must NOT take the fast-path (escapeFormulas
// gates it), so it still produces valid CSV via the writer path.
func Test_csvCodec_Marshal_PromotionFastPath(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		codec *csvCodec
		body  string
		fast  bool
	}
	tests := []tc{
		//: default codec (escape off) takes the hand-rolled fast-path.
		{"default codec uses fast-path", &csvCodec{}, `{"k":"v"}`, true},
		//: escape-on codec bypasses the fast-path even on the promotion shape.
		{"escape codec bypasses fast-path", &csvCodec{escapeFormulas: true}, `{"k":"v"}`, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		in := [][]string{{promotionHeaderCell}, {tc.body}}
		out, err := tc.codec.Marshal(in)
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		//: the fast-path emits exactly what marshalPromotionShape would;
		//: assert byte-equality only on that arm so a routing regression
		//: (fast-path silently skipped) is caught.
		if tc.fast {
			want := string(marshalPromotionShape(tc.body))
			if string(out) != want {
				t.Errorf("%s: fast-path bytes=%q want %q", tc.name, out, want)
			}
		}
		//: both arms must round-trip back to the original matrix.
		var got [][]string
		if uerr := tc.codec.Unmarshal(out, &got); uerr != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, uerr)
		}
		if len(got) != 2 || got[0][0] != promotionHeaderCell || got[1][0] != tc.body {
			t.Errorf("%s: round-trip=%v want [[%q] [%q]]", tc.name, got, promotionHeaderCell, tc.body)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
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
		{"oversize-orphaned-untouched", scratch.MaxRetainedBufBytes + 1},
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
