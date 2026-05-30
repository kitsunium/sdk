package transform_test

import (
	"bytes"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctransform "github.com/kitsunium/sdk/internal/service/transform"
)

func TestFlateRoundTrip(t *testing.T) {
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
		//: flate round-trip is bidirectional: Compress shrinks, Decompress restores.
		roundTrip(t, "flate", c.payload, c.wantShrink)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestFlateDecompressGarbage(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		input      []byte
		wantReason string
	}
	tests := []tc{
		{"non-flate bytes fail", []byte("\xff\xff\xff not a flate stream"), "FLATE_FAILED"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := resolve(t, "flate").Decompress(nil, c.input)
		//: garbage input must surface the flate failure reason.
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

func TestFlateRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		algo coretransform.Algorithm
		want coretransform.Algorithm
	}
	tests := []tc{{"flate resolves to canonical name", "flate", "flate"}}
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

// ensure the blank-import side effect is referenced for flate as well.
var _ = svctransform.FlateCompressor
