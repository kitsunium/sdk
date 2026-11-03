// Package transform_test — the registry's misses. What a registered engine
// must do is the conformance table's (conformance_external_test.go).
package transform_test

import (
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctransform "github.com/kitsunium/sdk/internal/service/data/transform"
)

// TestUnregisteredAlgorithm pins the miss: an Algorithm nobody registered,
// and the reserved empty one, resolve to nothing.
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
