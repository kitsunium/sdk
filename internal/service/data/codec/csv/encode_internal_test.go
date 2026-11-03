package csv

import (
	"bytes"
	stdcsv "encoding/csv"
	"testing"
)

// stdlibRecords is what encoding/csv's Writer writes for records with comma,
// the oracle appendRecords must equal; escape prefixes the cells the OWASP
// mitigation prefixes, on a copy, as Marshal did before it wrote in place.
func stdlibRecords(t *testing.T, records [][]string, comma rune, escape bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := stdcsv.NewWriter(&buf)
	w.Comma = comma
	for _, record := range records {
		row := record
		if escape {
			row = make([]string, len(record))
			for i, cell := range record {
				row[i] = cell
				if cell != "" && isFormulaTrigger(cell[0]) {
					row[i] = "'" + cell
				}
			}
		}
		if err := w.Write(row); err != nil {
			t.Fatalf("encoding/csv refused %q: %v", row, err)
		}
	}
	w.Flush()
	return buf.Bytes()
}

// encodeCells are the cells whose quoting encoding/csv decides one way or
// the other.
var encodeCells = []string{
	"", "plain", "a,b", "a;b", "a\tb", "a|b", "a¦b", `"`, `say "hi"`, "two\nlines", "cr\r", "\r\n",
	" lead", " nbsp", " em", `\.`, `\.x`, "=SUM(A1)", "+1", "-1", "@me", "\tTAB", "\rCR",
	"=\"q\"", "-a,b", "é", "\xff", "trail ",
}

// TestAppendRecordsIsEncodingCSV pins the hand-written encoder to the
// standard library's, byte for byte, over every cell above, each delimiter
// Options may name, with and without the formula mitigation.
func TestAppendRecordsIsEncodingCSV(t *testing.T) {
	t.Parallel()
	records := [][]string{{}, {""}, encodeCells}
	for _, cell := range encodeCells {
		records = append(records, []string{cell}, []string{cell, cell})
	}
	for _, comma := range []rune{',', ';', '\t', '|', '¦'} {
		for _, escape := range []bool{false, true} {
			want := stdlibRecords(t, records, comma, escape)
			got := appendRecords([]byte("PRE"), records, comma, escape)
			if !bytes.Equal(got[3:], want) {
				t.Fatalf("comma %q escape %v: wrote\n%q\nencoding/csv writes\n%q", comma, escape, got[3:], want)
			}
			if n := recordsLen(records, comma, escape); n != len(want) {
				t.Fatalf("comma %q escape %v: recordsLen = %d, written %d", comma, escape, n, len(want))
			}
		}
	}
}

// FuzzAppendRecordsIsEncodingCSV holds the same equality on any cells.
func FuzzAppendRecordsIsEncodingCSV(f *testing.F) {
	for _, cell := range encodeCells {
		f.Add(cell, "x", false)
	}
	f.Add("=1,\"2\"\n", " b", true)
	f.Fuzz(func(t *testing.T, a, b string, escape bool) {
		records := [][]string{{a, b}, {b}, {a}}
		for _, comma := range []rune{',', ';', '¦'} {
			want := stdlibRecords(t, records, comma, escape)
			if got := appendRecords(nil, records, comma, escape); !bytes.Equal(got, want) {
				t.Fatalf("comma %q escape %v: wrote %q, encoding/csv writes %q", comma, escape, got, want)
			}
			if n := recordsLen(records, comma, escape); n != len(want) {
				t.Fatalf("recordsLen = %d, written %d", n, len(want))
			}
		}
	})
}
