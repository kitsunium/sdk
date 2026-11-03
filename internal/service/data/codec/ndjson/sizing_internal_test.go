package ndjson

import "testing"

// FuzzUnmarshalCountsRecords holds the invariant #1098's sizing rests on: when
// Unmarshal succeeds, it decoded exactly countNDJSONRecords(data) records —
// the count is never short (a slot missing) nor long (a slot per blank line).
// Seeded with the blank-line-heavy inputs the amplification came from.
func FuzzUnmarshalCountsRecords(f *testing.F) {
	for _, seed := range []string{"", "\n\n\n", " \t\r\n\n", "1\n\n2", "{}\n \n[]\r\n", " \n1", "\"a\"\n\n\n\n"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var out []any
		if err := Codec.Unmarshal(data, &out); err != nil {
			return
		}
		if want := countNDJSONRecords(data); len(out) != want {
			t.Fatalf("decoded %d records, countNDJSONRecords says %d", len(out), want)
		}
	})
}
