package transform

import (
	"bytes"
	"testing"
)

// Test_gzipCompressor_Algorithm pins the scheme's canonical identifier.
func Test_gzipCompressor_Algorithm(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"algorithm is gzip", "gzip"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the concrete scheme must report its canonical Algorithm.
		if got := (gzipCompressor{}).Algorithm(); string(got) != c.want {
			t.Errorf("%s: Algorithm()=%q want %q", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_gzipCompressor_Compress exercises the encode path: a real payload
// produces non-empty wire bytes, and a repetitive payload shrinks.
func Test_gzipCompressor_Compress(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		payload    []byte
		wantShrink bool
	}
	tests := []tc{
		{"small payload encodes", []byte("compress me"), false},
		{"repetitive payload shrinks", bytes.Repeat([]byte("AAAA"), 8192), true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		boxed, err := (gzipCompressor{}).Compress(nil, c.payload)
		if err != nil {
			t.Fatalf("%s: Compress err=%v", c.name, err)
		}
		//: a repetitive payload must compress strictly smaller than its input.
		if c.wantShrink && len(boxed) >= len(c.payload) {
			t.Errorf("%s: compressed %d >= input %d", c.name, len(boxed), len(c.payload))
		}
		//: any non-empty payload must produce a non-empty gzip frame.
		if len(boxed) == 0 {
			t.Errorf("%s: empty gzip frame for non-empty payload", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_gzipCompressor_Decompress exercises the decode path: a round-trip
// reproduces the original, and garbage input surfaces GzipFailed.
func Test_gzipCompressor_Decompress(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		payload []byte
		garbage bool
	}
	tests := []tc{
		{"round-trip empty", []byte{}, false},
		{"round-trip text", []byte("decompress me back"), false},
		{"garbage fails", []byte("not a gzip frame at all"), true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := gzipCompressor{}
		//: the garbage arm feeds raw bytes straight into Decompress.
		if c.garbage {
			if _, err := g.Decompress(nil, c.payload); err == nil {
				t.Errorf("%s: expected error on garbage, got nil", c.name)
			}
			return
		}
		boxed, cerr := g.Compress(nil, c.payload)
		if cerr != nil {
			t.Fatalf("%s: seed Compress err=%v", c.name, cerr)
		}
		got, derr := g.Decompress(nil, boxed)
		if derr != nil {
			t.Fatalf("%s: Decompress err=%v", c.name, derr)
		}
		//: an empty payload round-trips to an empty slice.
		if len(c.payload) == 0 && len(got) == 0 {
			return
		}
		if !bytes.Equal(got, c.payload) {
			t.Errorf("%s: round-trip mismatch", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
