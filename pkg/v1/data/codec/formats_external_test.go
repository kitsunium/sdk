// Package codec_test — the per-format packages beside the full registry: a
// program importing both registers each format once and panics at no import.
package codec_test

import (
	"slices"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec"
	codecasn1 "github.com/kitsunium/sdk/pkg/v1/data/codec/asn1"
	codecbaseenc "github.com/kitsunium/sdk/pkg/v1/data/codec/baseenc"
	codecbson "github.com/kitsunium/sdk/pkg/v1/data/codec/bson"
	codeccbor "github.com/kitsunium/sdk/pkg/v1/data/codec/cbor"
	codeccsv "github.com/kitsunium/sdk/pkg/v1/data/codec/csv"
	codecflatbuffers "github.com/kitsunium/sdk/pkg/v1/data/codec/flatbuffers"
	codecform "github.com/kitsunium/sdk/pkg/v1/data/codec/form"
	codecjson "github.com/kitsunium/sdk/pkg/v1/data/codec/json"
	codecmsgpack "github.com/kitsunium/sdk/pkg/v1/data/codec/msgpack"
	codecmultipart "github.com/kitsunium/sdk/pkg/v1/data/codec/multipart"
	codecndjson "github.com/kitsunium/sdk/pkg/v1/data/codec/ndjson"
	codecpem "github.com/kitsunium/sdk/pkg/v1/data/codec/pem"
	codectlv "github.com/kitsunium/sdk/pkg/v1/data/codec/tlv"
	codectoml "github.com/kitsunium/sdk/pkg/v1/data/codec/toml"
	codecxml "github.com/kitsunium/sdk/pkg/v1/data/codec/xml"
	codecyaml "github.com/kitsunium/sdk/pkg/v1/data/codec/yaml"
)

// perFormat is every name the per-format packages publish: the twenty-four
// Formats of the full registry, each from the package that registers it.
var perFormat = []string{
	codecasn1.Format,
	codecbaseenc.Base64, codecbaseenc.Base64URL, codecbaseenc.Base32, codecbaseenc.Base16,
	codecbaseenc.Hex, codecbaseenc.ASCII85, codecbaseenc.Base45, codecbaseenc.Base58, codecbaseenc.Base62,
	codecbson.Format,
	codeccbor.Format,
	codeccsv.Format,
	codecflatbuffers.Format,
	codecform.Format,
	codecjson.Format,
	codecmsgpack.Format,
	codecmultipart.Format,
	codecndjson.Format,
	codecpem.Format,
	codectlv.Format,
	codectoml.Format,
	codecxml.Format,
	codecyaml.Format,
}

// TestAFormatImportedTwiceIsRegisteredOnce pins that the per-format packages
// and this one can be imported together: registering a duplicate panics, and
// this test binary imports all seventeen, so reaching this test at all is half
// the proof. The other half is the registry, which lists each format once,
// under the name its per-format package publishes, and nothing else.
func TestAFormatImportedTwiceIsRegisteredOnce(t *testing.T) {
	t.Parallel()
	available := codec.Available()
	//: each per-format package's name, registered exactly once.
	for _, name := range perFormat {
		//: counted in the registry.
		if count := countFormat(available, codec.Format(name)); count != 1 {
			t.Errorf("%q is registered %d times", name, count)
		}
	}
	//: and the aggregate registers nothing the per-format packages do not.
	if len(available) != len(perFormat) {
		t.Errorf("the registry holds %d formats, the per-format packages publish %d", len(available), len(perFormat))
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
