// Package codec_test — the per-format packages beside the full registry: a
// program importing both registers each format once and panics at no import.
package codec_test

import (
	"bytes"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

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

// claimsZero says it is zero whatever its bits hold, so omitzero must ask it.
type claimsZero struct {
	// N keeps reflect's zero test false when set.
	N int
}

// IsZero is the type's own notion of zero.
func (claimsZero) IsZero() bool { return true }

// fiveOmitZero tags the same five fields omitzero for every format that reads
// struct tags of its own.
type fiveOmitZero struct {
	T time.Time  `json:"t,omitzero" cbor:"t,omitzero" toml:"t,omitzero" yaml:"t,omitzero" msgpack:"t,omitzero" bson:"t,omitzero"`
	S struct{}   `json:"s,omitzero" cbor:"s,omitzero" toml:"s,omitzero" yaml:"s,omitzero" msgpack:"s,omitzero" bson:"s,omitzero"`
	A claimsZero `json:"a,omitzero" cbor:"a,omitzero" toml:"a,omitzero" yaml:"a,omitzero" msgpack:"a,omitzero" bson:"a,omitzero"`
	N int        `json:"n,omitzero" cbor:"n,omitzero" toml:"n,omitzero" yaml:"n,omitzero" msgpack:"n,omitzero" bson:"n,omitzero"`
	P *int       `json:"p,omitzero" cbor:"p,omitzero" toml:"p,omitzero" yaml:"p,omitzero" msgpack:"p,omitzero" bson:"p,omitzero"`
}

// TestOmitZeroAgreesAcrossFormats is #1103's matrix: one Go value, one set of
// members written, whichever of the six struct-tag formats it is promoted to.
func TestOmitZeroAgreesAcrossFormats(t *testing.T) {
	t.Parallel()
	seven := 7
	when := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	formats := []string{codecjson.Format, codeccbor.Format, codectoml.Format, codecyaml.Format, codecmsgpack.Format, codecbson.Format}
	for _, tc := range []struct {
		value fiveOmitZero
		want  []string
	}{
		{fiveOmitZero{A: claimsZero{N: 5}}, nil},
		{fiveOmitZero{T: when, N: 7, P: &seven}, []string{"n", "p", "t"}},
	} {
		for _, format := range formats {
			data, err := codec.Marshal(codec.Format(format), tc.value)
			if err != nil {
				t.Fatalf("Marshal(%s): %v", format, err)
			}
			var back map[string]any
			if err := codec.Unmarshal(codec.Format(format), data, &back); err != nil {
				t.Fatalf("Unmarshal(%s): %v", format, err)
			}
			if got := slices.Sorted(maps.Keys(back)); !slices.Equal(got, tc.want) {
				t.Errorf("%s wrote %q, want %q", format, got, tc.want)
			}
		}
	}
}

// durationDoc carries one time.Duration under the same key in every format
// that reads struct tags of its own.
type durationDoc struct {
	D time.Duration `json:"d" cbor:"d" toml:"d" yaml:"d" msgpack:"d" bson:"d" xml:"d"`
}

// asInt64 returns the integer an untyped decode produced, whichever integer
// type the format decodes to, and whether it was one.
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case uint64:
		return int64(n), n <= 1<<63-1
	case int32:
		return int64(n), true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}

// TestDurationAgreesAcrossFormats is #1126's matrix: one time.Duration, one
// rule. Every registered Format round-trips it; the hand-edited text formats
// (yaml, toml) write its String(), json keeps encoding/json's integer, and the
// binary formats (cbor, msgpack, bson) write int64 nanoseconds.
func TestDurationAgreesAcrossFormats(t *testing.T) {
	t.Parallel()
	in := durationDoc{D: 90 * time.Minute}
	for _, f := range codec.Available() {
		data, err := codec.Marshal(f, in)
		if err != nil {
			t.Errorf("Marshal(%s): %v", f, err)
			continue
		}
		var back durationDoc
		//: every format reads back what it wrote.
		if err := codec.Unmarshal(f, data, &back); err != nil || back != in {
			t.Errorf("%s: round trip = %v, %v; want %v", f, back, err, in)
		}
	}
	text := []byte(in.D.String())
	nanos := []byte(strconv.FormatInt(in.D.Nanoseconds(), 10))
	for _, tc := range []struct {
		format string
		want   []byte
	}{
		{codecyaml.Format, text},
		{codectoml.Format, text},
		{codecjson.Format, nanos},
	} {
		data, err := codec.Marshal(codec.Format(tc.format), in)
		//: the text formats write the text, json the integer.
		if err != nil || !bytes.Contains(data, tc.want) {
			t.Errorf("%s wrote %q (%v), want it to hold %q", tc.format, data, err, tc.want)
		}
	}
	for _, format := range []string{codeccbor.Format, codecmsgpack.Format, codecbson.Format} {
		data, err := codec.Marshal(codec.Format(format), in)
		if err != nil {
			t.Fatalf("Marshal(%s): %v", format, err)
		}
		var back map[string]any
		if err := codec.Unmarshal(codec.Format(format), data, &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", format, err)
		}
		//: the binary formats write the nanoseconds as an integer.
		if got, ok := asInt64(back["d"]); !ok || got != in.D.Nanoseconds() {
			t.Errorf("%s wrote d = %#v, want the integer %d", format, back["d"], in.D.Nanoseconds())
		}
	}
}

// TestDurationTextCrossesYAMLAndTOML pins that a Duration read from one
// hand-edited format is written by the other in the same words: a
// configuration converted between the two keeps "30m" meaning thirty minutes.
func TestDurationTextCrossesYAMLAndTOML(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		from, to string
		doc      string
		want     string
	}{
		{codecyaml.Format, codectoml.Format, "d: 1h30m0s\n", "d = '1h30m0s'\n"},
		{codectoml.Format, codecyaml.Format, "d = '45s'\n", "d: 45s\n"},
	} {
		var mid durationDoc
		if err := codec.Unmarshal(codec.Format(tc.from), []byte(tc.doc), &mid); err != nil {
			t.Fatalf("Unmarshal(%s, %q): %v", tc.from, tc.doc, err)
		}
		out, err := codec.Marshal(codec.Format(tc.to), mid)
		//: the same text, in the other format's syntax.
		if err != nil || string(out) != tc.want {
			t.Errorf("%s -> %s wrote %q (%v), want %q", tc.from, tc.to, out, err, tc.want)
		}
	}
}
