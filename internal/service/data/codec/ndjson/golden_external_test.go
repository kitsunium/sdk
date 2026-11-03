package ndjson_test

import (
	"bytes"
	stdjson "encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/ndjson"
)

// updateGolden rewrites testdata/golden from the codec under test. It is how
// the files were written — on the base, before #893 moved the codec onto
// encoding/json/v2 — and must not be run to make a failing gate pass.
var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/golden from the codec under test")

// goldenPtr marshals itself through its pointer only: an addressable slice
// element calls it.
type goldenPtr struct{ N int }

// MarshalJSON writes a marker.
func (g *goldenPtr) MarshalJSON() ([]byte, error) { return []byte(`"PTR"`), nil }

// goldenEvent is a record with the members v1 semantics spell.
type goldenEvent struct {
	ID      int64              `json:"id"`
	Kind    string             `json:"kind"`
	HTML    string             `json:"html"`
	Bytes   []byte             `json:"bytes"`
	Nil     []string           `json:"nil"`
	Attrs   map[string]string  `json:"attrs,omitempty"`
	At      time.Time          `json:"at"`
	Quoted  float64            `json:"quoted,string"`
	Raw     stdjson.RawMessage `json:"raw"`
	Invalid string             `json:"invalid"`
}

// goldenBatches are the batches whose NDJSON is pinned, by golden file name.
func goldenBatches() map[string]any {
	at := time.Date(2026, 11, 3, 8, 0, 0, 1, time.UTC)
	return map[string]any{
		"events": []goldenEvent{
			{ID: 1, Kind: "a", HTML: "<&>", Bytes: []byte{1, 2}, At: at, Quoted: 0.1, Raw: stdjson.RawMessage(`{ "x" : 1 }`), Invalid: "\xff"},
			{ID: math.MaxInt64, Kind: " ", Attrs: map[string]string{"z": "1", "a": "2"}, At: at, Raw: stdjson.RawMessage(`null`)},
		},
		"any":          []any{1, "s", nil, map[string]any{"b": 1, "a": []any{}}, 1e21, true},
		"pointer-recv": []goldenPtr{{1}, {2}},
		"raw-rows":     [][]byte{[]byte(`{"pre":1}`), []byte("{\n\"multi\": \"line\"\n}")},
	}
}

// goldenFile is a golden file's path.
func goldenFile(name string) string {
	return filepath.Join("testdata", "golden", name+".ndjson")
}

// TestNDJSONIsGolden is #893's byte-identity gate for ndjson: each batch
// encodes — Marshal, Append, record by record through the stream encoder —
// to the bytes the codec wrote on encoding/json v1, and decodes back, line by
// line, to what re-encodes to those bytes.
func TestNDJSONIsGolden(t *testing.T) {
	c := ndjson.New()
	a, _ := c.(corecodec.Appender)
	for name, batch := range goldenBatches() {
		got, err := c.Marshal(batch)
		if err != nil {
			t.Fatalf("%s: Marshal: %v", name, err)
		}
		if *updateGolden {
			if err := os.WriteFile(goldenFile(name), got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(goldenFile(name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: the codec wrote\n%s\nencoding/json v1 wrote\n%s", name, got, want)
		}
		if appended, aerr := a.Append(nil, batch); aerr != nil || !bytes.Equal(appended, want) {
			t.Fatalf("%s: Append wrote %q, %v", name, appended, aerr)
		}
		var back []any
		if uerr := c.Unmarshal(want, &back); uerr != nil {
			t.Fatalf("%s: Unmarshal: %v", name, uerr)
		}
		again, _ := c.Marshal(back)
		var reread []any
		if uerr := c.Unmarshal(again, &reread); uerr != nil || len(reread) != len(back) {
			t.Fatalf("%s: a decoded batch did not re-read: %v", name, uerr)
		}
	}
}
