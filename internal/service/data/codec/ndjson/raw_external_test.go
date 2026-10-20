package ndjson_test

import (
	stdjson "encoding/json"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corendjson "github.com/kitsunium/sdk/internal/core/data/codec/ndjson"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/ndjson"
)

// TestPreEncodedRowsStayJSONWhateverTheirContent pins #462: pre-encoded rows
// — [][]byte and []json.RawMessage — are JSON records in the output whether
// or not a row carries a newline. A row with one, any pretty-printed
// document, used to send [][]byte down the reflect path, where encoding/json
// writes every []byte as a base64 string: the format changed with the
// content and did not decode back into the records. Marshal and Append both.
func TestPreEncodedRowsStayJSONWhateverTheirContent(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		rows [][]byte
		want string
	}{
		"no newline, written as given": {
			rows: [][]byte{[]byte(`{"a":1}`), []byte(`{ "b" : 2 }`)},
			want: "{\"a\":1}\n{ \"b\" : 2 }\n",
		},
		"one pretty-printed row": {
			rows: [][]byte{[]byte(`{"a":1}`), []byte("{\n  \"b\": 2\n}")},
			want: "{\"a\":1}\n{\"b\":2}\n",
		},
		"every row pretty-printed": {
			rows: [][]byte{[]byte("[\n1,\n2\n]"), []byte("\"x\"\n"), []byte("\n3")},
			want: "[1,2]\n\"x\"\n3\n",
		},
		"a newline inside a string stays escaped": {
			rows: [][]byte{[]byte("{\n\"s\": \"a\\nb\"}")},
			want: "{\"s\":\"a\\nb\"}\n",
		},
		"no rows": {rows: [][]byte{}, want: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raws := make([]stdjson.RawMessage, len(tc.rows))
			for i, r := range tc.rows {
				raws[i] = stdjson.RawMessage(r)
			}
			for shape, v := range map[string]any{"[][]byte": tc.rows, "[]json.RawMessage": raws} {
				out, err := ndjson.Codec.Marshal(v)
				if err != nil {
					t.Fatalf("%s: Marshal() = %v", shape, err)
				}
				if string(out) != tc.want {
					t.Fatalf("%s: Marshal()\n got  %q\n want %q", shape, out, tc.want)
				}
				appended, err := ndjson.Codec.(codec.Appender).Append([]byte("prior\n"), v)
				if err != nil || string(appended) != "prior\n"+tc.want {
					t.Fatalf("%s: Append() = %q, %v", shape, appended, err)
				}
				var back []any
				if err := ndjson.Codec.Unmarshal(out, &back); err != nil || len(back) != len(tc.rows) {
					t.Fatalf("%s: the output does not decode into %d records: %v, %v", shape, len(tc.rows), back, err)
				}
			}
		})
	}
}

// TestAPreEncodedRowThatIsNotJSONIsRefused pins the other side of the slow
// path: a row carrying a newline is validated, so one that is not JSON is
// MARSHAL_FAILED — never written out as a base64 string, never torn into
// Append's buffer.
func TestAPreEncodedRowThatIsNotJSONIsRefused(t *testing.T) {
	t.Parallel()
	rows := [][]byte{[]byte(`{"a":1}`), []byte("{\n not json")}
	if _, err := ndjson.Codec.Marshal(rows); !errs.HasCode(err, corendjson.CodeNDJSONMarshalFailed) {
		t.Fatalf("Marshal() = %v, want MARSHAL_FAILED", err)
	}
	dst := []byte("prior\n")
	out, err := ndjson.Codec.(codec.Appender).Append(dst, rows)
	if !errs.HasCode(err, corendjson.CodeNDJSONMarshalFailed) || string(out) != "prior\n" {
		t.Fatalf("Append() = %q, %v; want the prior contents and MARSHAL_FAILED", out, err)
	}
}
