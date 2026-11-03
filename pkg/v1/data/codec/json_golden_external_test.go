package codec_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec"
)

// updateJSONGolden rewrites testdata/json-golden from the codec under test:
// how the files were written, on the base before #893, never how a failing
// gate is fixed.
var updateJSONGolden = flag.Bool("update-json-golden", false, "rewrite testdata/json-golden")

// TestComplexRTJSONIsGolden is #893's gate on the facade's fixture: complexRT
// through codec.JSON, and three of them through codec.NDJSON, encode to the
// bytes encoding/json v1 wrote on the base, and read back to equal values.
func TestComplexRTJSONIsGolden(t *testing.T) {
	base := sampleComplex()
	cases := map[string]struct {
		format codec.Format
		value  any
	}{
		"complex.json":   {codec.JSON, base},
		"complex.ndjson": {codec.NDJSON, []complexRT{base, nextInt(base), nextInt(nextInt(base))}},
	}
	for name, tc := range cases {
		got, err := codec.Marshal(tc.format, tc.value)
		if err != nil {
			t.Fatalf("%s: Marshal: %v", name, err)
		}
		path := filepath.Join("testdata", "json-golden", name)
		if *updateJSONGolden {
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: wrote\n%s\nencoding/json v1 wrote\n%s", name, got, want)
		}
	}
	var back complexRT
	if err := codec.Unmarshal(codec.JSON, mustRead(t, "complex.json"), &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	again, _ := codec.Marshal(codec.JSON, back)
	if !bytes.Equal(again, mustRead(t, "complex.json")) {
		t.Fatal("complexRT did not read back to the bytes it was written as")
	}
}

// mustRead reads a golden file.
func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "json-golden", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
