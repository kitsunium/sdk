package transform_test

import (
	"bytes"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctransform "github.com/kitsunium/sdk/internal/service/transform"
)

// resolve fetches a registered compressor by algorithm, failing the test when a
// scheme that must self-register on import is missing.
func resolve(t *testing.T, algo coretransform.Algorithm) coretransform.Compressor {
	t.Helper()
	//: blank-importing svctransform must have registered both schemes.
	c, ok := coretransform.Lookup(algo)
	//: absence means the package-level Register var did not run on import.
	if !ok {
		t.Fatalf("%q compressor not registered via import", algo)
	}
	//: hand the resolved scheme back to the caller.
	return c
}

// roundTrip drives Compress then Decompress through the registry-resolved scheme
// and asserts the payload survives intact (shared by the gzip + flate suites).
func roundTrip(t *testing.T, algo coretransform.Algorithm, payload []byte, wantShrink bool) {
	t.Helper()
	comp := resolve(t, algo)
	//: forward direction — Compress produces the wire bytes.
	boxed, cerr := comp.Compress(nil, payload)
	if cerr != nil {
		t.Fatalf("%s: Compress err=%v", algo, cerr)
	}
	//: a repetitive payload must compress strictly smaller than its input.
	if wantShrink && len(boxed) >= len(payload) {
		t.Errorf("%s: compressed %d >= input %d", algo, len(boxed), len(payload))
	}
	//: reverse direction — Decompress must reproduce the original payload.
	got, derr := comp.Decompress(nil, boxed)
	if derr != nil {
		t.Fatalf("%s: Decompress err=%v", algo, derr)
	}
	//: an empty payload round-trips to an empty (possibly nil) slice.
	if len(payload) == 0 && len(got) == 0 {
		return
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("%s: round-trip mismatch (got %d, want %d)", algo, len(got), len(payload))
	}
}

func TestGzipRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		payload    []byte
		wantShrink bool
	}
	tests := []tc{
		{"empty", []byte{}, false},
		{"small", []byte("hello, transform"), false},
		{"repetitive shrinks", bytes.Repeat([]byte("kitsunium-sdk "), 4096), true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: gzip round-trip is bidirectional: Compress shrinks, Decompress restores.
		roundTrip(t, "gzip", c.payload, c.wantShrink)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestGzipDecompressGarbage(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		input      []byte
		wantReason string
	}
	tests := []tc{
		{"non-gzip bytes fail", []byte("definitely not a gzip frame"), "GZIP_FAILED"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := resolve(t, "gzip").Decompress(nil, c.input)
		//: garbage input must surface the gzip failure reason.
		if !errs.HasReason(err, c.wantReason) {
			t.Errorf("%s: expected %s, got %v", c.name, c.wantReason, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestGzipRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		algo coretransform.Algorithm
		want coretransform.Algorithm
	}
	tests := []tc{{"gzip resolves to canonical name", "gzip", "gzip"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the resolved scheme must report its canonical Algorithm.
		if got := resolve(t, c.algo).Algorithm(); got != c.want {
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

func TestUnregisteredAlgorithm(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		algo coretransform.Algorithm
	}
	tests := []tc{
		{"absent scheme misses", "zstd-not-registered"},
		{"empty algorithm misses", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: Lookup of an unregistered Algorithm must miss.
		if _, ok := coretransform.Lookup(c.algo); ok {
			t.Errorf("%s: Lookup(%q) unexpectedly resolved", c.name, c.algo)
		}
		//: the core sentinel for an unregistered scheme carries UNKNOWN_COMPRESSOR.
		if !errs.HasReason(coretransform.UnknownCompressor, "UNKNOWN_COMPRESSOR") {
			t.Errorf("%s: UnknownCompressor sentinel reason mismatch", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// ensure the blank-import side effect is referenced so the test binary links
// the package even if a future refactor drops the direct symbol uses.
var _ = svctransform.GzipCompressor
