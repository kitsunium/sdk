package csv_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/csv"
)

// FuzzUnmarshalRoundTrip is #897's csv target: any input decodes without a
// panic; the records it yields re-encode, and read back equal — through the
// registered codec, and through NewWithEscape(true), whose output never holds
// a cell a spreadsheet would run as a formula. A record of one empty field is
// the one encoding/csv cannot carry, and is left out; so is the carriage
// return its reader drops before a line feed.
func FuzzUnmarshalRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"a,b\n1,2\n", "\"quoted, comma\",\"line\nbreak\"\n", "=SUM(A1),+1,-1,@x\n", "a\r\nb\r\n", "\"unterminated\n", "", "a,b\n1\n",
	} {
		f.Add([]byte(seed))
	}
	plain, escaped := csv.New(), csv.NewWithEscape(true)
	f.Fuzz(func(t *testing.T, data []byte) {
		var records [][]string
		//: decoded, or refused — never a panic.
		if err := plain.Unmarshal(data, &records); err != nil || len(records) == 0 {
			return
		}
		//: a record of one empty field is written as an empty line, which every
		//: reader skips — encoding/csv's own bytes, which this encoder keeps
		//: (FuzzAppendRecordsIsEncodingCSV): such a record does not survive a
		//: round trip, by the standard library's design.
		if slices.ContainsFunc(records, func(r []string) bool { return len(r) == 1 && r[0] == "" }) {
			return
		}
		out, err := plain.Marshal(records)
		if err != nil {
			t.Fatalf("Marshal of decoded records: %v", err)
		}
		var again [][]string
		//: the records read back as written, but for the carriage return
		//: encoding/csv's reader drops before a line feed, quoted or not.
		if err := plain.Unmarshal(out, &again); err != nil || !slices.EqualFunc(again, records, sameCells) {
			t.Fatalf("round trip of %q: %q (%v), want %q", out, again, err, records)
		}
		safe, err := escaped.Marshal(records)
		if err != nil {
			t.Fatalf("escaped Marshal: %v", err)
		}
		var cells [][]string
		if err := plain.Unmarshal(safe, &cells); err != nil {
			t.Fatalf("Unmarshal of the escaped output %q: %v", safe, err)
		}
		//: no cell opens on a formula character.
		for _, row := range cells {
			for _, cell := range row {
				if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
					t.Fatalf("escaped output %q holds the cell %q", safe, cell)
				}
			}
		}
	})
}

// sameCells reports whether got is want once each "\r\n" in want is the
// "\n" encoding/csv reads it back as.
func sameCells(got, want []string) bool {
	//: cell by cell.
	return slices.EqualFunc(got, want, func(g, w string) bool { return g == strings.ReplaceAll(w, "\r\n", "\n") })
}
