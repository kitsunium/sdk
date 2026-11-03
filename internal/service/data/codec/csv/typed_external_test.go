package csv_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	corecsv "github.com/kitsunium/sdk/internal/core/data/codec/csv"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/csv"
)

// abRow is #902's acceptance shape.
type abRow struct {
	A int    `csv:"a"`
	B string `csv:"b"`
}

// level is a type that reads and writes itself as text.
type level int

// MarshalText writes the level's name.
func (l level) MarshalText() ([]byte, error) { return []byte([]string{"low", "high"}[l]), nil }

// UnmarshalText reads a level's name.
func (l *level) UnmarshalText(b []byte) error {
	switch string(b) {
	case "low":
		*l = 0
	case "high":
		*l = 1
	default:
		return errors.New("unknown level")
	}
	return nil
}

// Embedded lends its fields to the row embedding it.
type Embedded struct {
	Zone string `csv:"zone"`
}

// fullRow maps a column of every kind.
type fullRow struct {
	Embedded
	Name    string    `csv:"name"`
	Count   int64     `csv:"count"`
	Small   uint8     `csv:"small"`
	Ratio   float64   `csv:"ratio"`
	OK      bool      `csv:"ok"`
	At      time.Time `csv:"at"`
	Maybe   *int      `csv:"maybe"`
	Level   level     `csv:"level"`
	Note    string    `csv:"note,optional"`
	Skipped string    `csv:"-"`
	hidden  string
	Untaged int
}

// TestUnmarshalTypedRows is #902's acceptance: a header and three rows read
// into a slice of structs, each cell converted to its field; a file without
// column a is HEADER_MISMATCH.
func TestUnmarshalTypedRows(t *testing.T) {
	t.Parallel()
	var rows []abRow
	if err := csv.New().Unmarshal([]byte("a,b,extra\n1,x,?\n2,y,?\n3,z,?\n"), &rows); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if want := []abRow{{1, "x"}, {2, "y"}, {3, "z"}}; len(rows) != 3 || rows[0] != want[0] || rows[1] != want[1] || rows[2] != want[2] {
		t.Fatalf("rows = %+v", rows)
	}
	err := csv.New().Unmarshal([]byte("b\nx\n"), &rows)
	if !errs.HasCode(err, corecsv.CodeCSVHeaderMismatch) {
		t.Fatalf("missing column a: %v, want HEADER_MISMATCH", err)
	}
	var ptrs []*abRow
	if err := csv.New().Unmarshal([]byte("\uFEFFb,a\ny,2\n"), &ptrs); err != nil || len(ptrs) != 1 || *ptrs[0] != (abRow{2, "y"}) {
		t.Fatalf("pointer rows, reordered columns, BOM: %+v, %v", ptrs, err)
	}
}

// TestTypedRoundTrip writes a slice of structs and reads it back: every
// kind of column, a nil pointer, an embedded struct, the header from the
// tags, the Go name of an untagged field.
func TestTypedRoundTrip(t *testing.T) {
	t.Parallel()
	seven := 7
	at := time.Date(2026, 11, 3, 12, 0, 0, 5, time.UTC)
	in := []fullRow{
		{Embedded: Embedded{"eu"}, Name: "a,b", Count: -3, Small: 255, Ratio: 0.1, OK: true, At: at, Maybe: &seven, Level: 1, Note: "=x", Skipped: "no", hidden: "no", Untaged: 4},
		{Name: " lead", At: at},
	}
	for _, c := range []corecodec.Codec{csv.New(), mustOptions(t, csv.Options{Comma: ';'}), mustOptions(t, csv.Options{NoHeader: true, Comma: '¦'})} {
		data, err := c.Marshal(in)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var out []fullRow
		if err := c.Unmarshal(data, &out); err != nil {
			t.Fatalf("Unmarshal of %q: %v", data, err)
		}
		if len(out) != 2 || out[0].Zone != "eu" || out[0].Name != "a,b" || out[0].Count != -3 || out[0].Small != 255 || out[0].Ratio != 0.1 ||
			!out[0].OK || !out[0].At.Equal(at) || *out[0].Maybe != 7 || out[0].Level != 1 || out[0].Untaged != 4 || out[0].Skipped != "" ||
			out[1].Maybe != nil || out[1].Name != " lead" {
			t.Fatalf("round trip of %q gave %+v", data, out)
		}
	}
	data, _ := csv.New().Marshal(in[:1])
	if header, _, _ := strings.Cut(string(data), "\n"); header != "zone,name,count,small,ratio,ok,at,maybe,level,note,Untaged" {
		t.Fatalf("header = %q", header)
	}
	escaped, _ := mustOptions(t, csv.Options{Escape: true}).Marshal(in[:1])
	if !strings.Contains(string(escaped), ",'=x,") || !strings.Contains(string(escaped), ",'-3,") {
		t.Fatalf("the formula mitigation did not reach the typed cells: %q", escaped)
	}
}

// mustOptions builds a codec from o.
func mustOptions(t *testing.T, o csv.Options) corecodec.Codec {
	t.Helper()
	c, err := csv.NewWithOptions(o)
	if err != nil {
		t.Fatalf("NewWithOptions(%+v): %v", o, err)
	}
	return c
}

// TestTypedRefusals pins the typed mapping's refusals: a cell that does not
// convert names its record and column and never the cell; a struct with a
// field no cell holds, or two fields on one column, is VALUE_INVALID; a
// delimiter encoding/csv refuses is OPTIONS_INVALID.
func TestTypedRefusals(t *testing.T) {
	t.Parallel()
	var rows []abRow
	err := csv.New().Unmarshal([]byte("a,b\n1,x\nsecret-value,y\n"), &rows)
	if !errs.HasCode(err, corecsv.CodeCSVCellInvalid) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("bad cell: %v, want CELL_INVALID without the cell", err)
	}
	var e *errs.Error
	if !errors.As(err, &e) || !strings.Contains(e.Private(), "record") {
		t.Fatalf("bad cell: %v carries no record", err)
	}
	type withMap struct{ M map[string]int }
	if _, err := csv.New().Marshal([]withMap{{}}); !errs.HasCode(err, corecsv.CodeCSVValueInvalid) {
		t.Fatalf("a map field: %v, want VALUE_INVALID", err)
	}
	type twice struct {
		A int `csv:"x"`
		B int `csv:"x"`
	}
	if err := csv.New().Unmarshal([]byte("x\n1\n"), &[]twice{}); !errs.HasCode(err, corecsv.CodeCSVValueInvalid) {
		t.Fatalf("two fields on one column: %v, want VALUE_INVALID", err)
	}
	if err := csv.New().Unmarshal([]byte("a,a,b\n1,2,x\n"), &rows); !errs.HasCode(err, corecsv.CodeCSVHeaderMismatch) {
		t.Fatalf("a column twice in the header: %v, want HEADER_MISMATCH", err)
	}
	for _, comma := range []rune{'"', '\n', '\r', 0xFFFD, -1} {
		if _, err := csv.NewWithOptions(csv.Options{Comma: comma}); !errs.HasCode(err, corecsv.CodeCSVOptionsInvalid) {
			t.Fatalf("comma %q: %v, want OPTIONS_INVALID", comma, err)
		}
	}
	if err := csv.New().Unmarshal([]byte("_json\n\"[]\"\n"), &rows); !errs.HasCode(err, corecsv.CodeCSVValueInvalid) {
		t.Fatalf("the promotion container read into structs: %v, want VALUE_INVALID for the facade to unwrap", err)
	}
}

// streamingCSV is the codec as a StreamingCodec.
func streamingCSV(t *testing.T, c corecodec.Codec) corecodec.StreamingCodec {
	t.Helper()
	sc, ok := c.(corecodec.StreamingCodec)
	if !ok {
		t.Fatal("csv does not implement codec.StreamingCodec")
	}
	return sc
}

// TestStreamDecodesOneRecordPerDecode is #902's stream acceptance: one record
// per Decode, More false at the end, into a []string as it is and into a
// struct through the header.
func TestStreamDecodesOneRecordPerDecode(t *testing.T) {
	t.Parallel()
	const input = "a,b\n1,x\n2,y\n3,z\n"
	raw := streamingCSV(t, csv.New()).NewDecoder(strings.NewReader(input))
	var records [][]string
	for raw.More() {
		var rec []string
		if err := raw.Decode(&rec); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		records = append(records, rec)
	}
	if len(records) != 4 || records[0][0] != "a" || records[3][1] != "z" {
		t.Fatalf("raw records %v", records)
	}
	typed := streamingCSV(t, csv.New()).NewDecoder(strings.NewReader(input))
	var mores []bool
	var rows []abRow
	for {
		more := typed.More()
		mores = append(mores, more)
		if !more {
			break
		}
		var row abRow
		if err := typed.Decode(&row); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		rows = append(rows, row)
	}
	if len(rows) != 3 || rows[2] != (abRow{3, "z"}) {
		t.Fatalf("typed rows %+v", rows)
	}
	if len(mores) != 4 || mores[3] {
		t.Fatalf("More reported %v", mores)
	}
	var row abRow
	if err := typed.Decode(&row); err != io.EOF {
		t.Fatalf("Decode past the end = %v, want io.EOF", err)
	}
	if err := streamingCSV(t, csv.New()).NewDecoder(strings.NewReader(input)).Decode(new(int)); !errs.HasCode(err, corecsv.CodeCSVValueInvalid) {
		t.Fatalf("an int target: %v, want VALUE_INVALID", err)
	}
	bad := streamingCSV(t, csv.New()).NewDecoder(strings.NewReader("a,b\n1,x\n\"unterminated\n"))
	_ = bad.Decode(&row)
	if err := bad.Decode(&row); !errs.HasCode(err, corecsv.CodeCSVUnmarshalFailed) {
		t.Fatalf("malformed CSV: %v, want UNMARSHAL_FAILED", err)
	}
}

// TestStreamEncoderWritesMarshalsTable pins the encoder: structs encoded one
// by one write Marshal's table — the header once — and a []string a record.
func TestStreamEncoderWritesMarshalsTable(t *testing.T) {
	t.Parallel()
	rows := []abRow{{1, "x"}, {2, "y,z"}}
	want, err := csv.New().Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := streamingCSV(t, csv.New()).NewEncoder(&buf)
	for i := range rows {
		if err := enc.Encode(&rows[i]); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	if err := enc.Close(); err != nil || !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("stream wrote %q, Marshal writes %q (%v)", buf.Bytes(), want, err)
	}
	buf.Reset()
	raw := streamingCSV(t, csv.New()).NewEncoder(&buf)
	if err := raw.Encode([]string{"p", "q r"}); err != nil || buf.String() != "p,q r\n" {
		t.Fatalf("a record: %q, %v", buf.String(), err)
	}
	if err := raw.Encode(42); !errs.HasCode(err, corecsv.CodeCSVValueInvalid) {
		t.Fatalf("an int: %v, want VALUE_INVALID", err)
	}
}
