// Package codec_test — the per-format packages beside the full registry: a
// program importing both registers each format once and panics at no import.
package codec_test

import (
	"slices"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/codec"
	codecjson "github.com/kitsunium/sdk/pkg/v1/codec/json"
	codectoml "github.com/kitsunium/sdk/pkg/v1/codec/toml"
	codecyaml "github.com/kitsunium/sdk/pkg/v1/codec/yaml"
)

// TestAFormatImportedTwiceIsRegisteredOnce pins that the per-format packages
// and this one can be imported together: registering a duplicate panics, and
// this test binary imports all four, so reaching this test at all is half the
// proof. The other half is the registry, which lists each format once and
// under the name each per-format package publishes.
func TestAFormatImportedTwiceIsRegisteredOnce(t *testing.T) {
	t.Parallel()
	available := codec.Available()
	//: each per-format package's name, registered exactly once.
	for _, name := range []string{codecjson.Format, codecyaml.Format, codectoml.Format} {
		//: counted in the registry.
		if count := countFormat(available, codec.Format(name)); count != 1 {
			t.Errorf("%q is registered %d times", name, count)
		}
	}
	var decoded map[string]any
	//: and the format works through the full facade's dispatch.
	if err := codec.Unmarshal(codecyaml.Format, []byte("name: kit\n"), &decoded); err != nil || decoded["name"] != "kit" {
		t.Errorf("Unmarshal(yaml) = %v, %v", decoded, err)
	}
}

// countFormat counts name in formats.
func countFormat(formats []codec.Format, name codec.Format) int {
	//: every occurrence.
	return len(slices.DeleteFunc(slices.Clone(formats), func(f codec.Format) bool { return f != name }))
}
