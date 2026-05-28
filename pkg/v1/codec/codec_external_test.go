package codec_test

import (
	"bytes"
	stdpem "encoding/pem"
	stdxml "encoding/xml"
	"errors"
	"flag"
	"io"
	"math"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/codec"
)

type event struct {
	Name  string `json:"name" xml:"name"`
	Count int    `json:"count" xml:"count"`
}

// appenderInfo is the package-level registry-entry record used by the
// KTN-TEST-REGISTRY-BIJECTION check in TestAppendRoundTrip_AllCodecs.
// The Name field is what the forward bijection loop reads to look up
// against expectedAppenders.
type appenderInfo struct {
	Name   string
	Format codec.Format
}

// expectedAppenders is the contractually-required list of codec Format
// names that must implement core/codec.Appender. The reverse-direction
// bijection loop in TestAppendRoundTrip_AllCodecs iterates this slice.
//
//nolint:gochecknoglobals // package-level slice required by KTN-TEST-REGISTRY-BIJECTION analyzer.
var expectedAppenders = []string{
	"json",
	"ndjson",
	"tlv",
	"flatbuffers",
	"xml",
	"yaml",
	"toml",
	"cbor",
	"msgpack",
	"asn1-der",
	"pem",
	"csv",
	"base64",
	"base64url",
	"base32",
	"base16",
	"hex",
	"ascii85",
}

// registeredAppenders is the package-level Appender registry snapshot
// computed from codec.Available() at init time. The forward-direction
// bijection loop in TestAppendRoundTrip_AllCodecs iterates this slice
// and reads each entry's .Name field.
//
//nolint:gochecknoglobals // package-level slice required by KTN-TEST-REGISTRY-BIJECTION analyzer.
var registeredAppenders = collectRegisteredAppenders()

// collectRegisteredAppenders builds the package-level Appender snapshot
// from the codec registry. Used once at package-var initialisation to
// populate registeredAppenders.
func collectRegisteredAppenders() []appenderInfo {
	var result []appenderInfo
	for _, f := range codec.Available() {
		c, ok := corecodec.Lookup(f)
		if !ok {
			continue
		}
		if _, isAppender := c.(corecodec.Appender); !isAppender {
			continue
		}
		result = append(result, appenderInfo{
			Name:   strings.ToLower(string(f)),
			Format: f,
		})
	}
	return result
}

// TestMarshal covers the facade Marshal dispatcher against every registered
// Format plus the UNKNOWN_FORMAT failure path.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		format  codec.Format
		value   any
		wantErr string // "" on success, reason string on expected failure
	}
	tests := []tc{
		{"json success", codec.JSON, event{Name: "ping", Count: 3}, ""},
		{"xml success", codec.XML, event{Name: "ping", Count: 1}, ""},
		{"yaml success", codec.YAML, map[string]int{"a": 1}, ""},
		{"toml success", codec.TOML, map[string]any{"a": 1}, ""},
		{"cbor success", codec.CBOR, map[string]int{"a": 1}, ""},
		{"msgpack success", codec.MsgPack, map[string]int{"a": 1}, ""},
		{"unknown format", codec.Format("nope"), 1, "UNKNOWN_FORMAT"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := codec.Marshal(tc.format, tc.value)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Marshal err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestUnmarshal covers the facade Unmarshal dispatcher end-to-end including
// the UNKNOWN_FORMAT failure path.
func TestUnmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		format  codec.Format
		data    []byte
		wantErr string
	}
	//: produce a JSON payload we can successfully decode in the happy case.
	jsonPayload, mErr := codec.Marshal(codec.JSON, event{Name: "ping", Count: 3})
	if mErr != nil {
		t.Fatalf("seed Marshal err = %v", mErr)
	}
	tests := []tc{
		{"json success", codec.JSON, jsonPayload, ""},
		{"unknown format", codec.Format("nope"), []byte("1"), "UNKNOWN_FORMAT"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var out event
		err := codec.Unmarshal(tc.format, tc.data, &out)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Unmarshal err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNewEncoder covers the streaming constructor dispatch against a
// streaming codec, a non-streaming codec, and an unknown Format.
func TestNewEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		format  codec.Format
		wantErr string
	}
	tests := []tc{
		{"json streaming ok", codec.JSON, ""},
		{"asn1 non-streaming", codec.ASN1DER, "STREAMING_UNSUPPORTED"},
		{"unknown format", codec.Format("nope"), "UNKNOWN_FORMAT"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := codec.NewEncoder(tc.format, io.Discard)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: NewEncoder err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNewDecoder covers the streaming decoder constructor dispatch.
func TestNewDecoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		format  codec.Format
		wantErr string
	}
	tests := []tc{
		{"json streaming ok", codec.JSON, ""},
		{"csv non-streaming", codec.CSV, "STREAMING_UNSUPPORTED"},
		{"unknown format", codec.Format("nope"), "UNKNOWN_FORMAT"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := codec.NewDecoder(tc.format, strings.NewReader(""))
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: NewDecoder err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRoundTripJSON exercises an end-to-end streaming JSON round-trip
// through the facade (several records written then read back).
func TestRoundTripJSON(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		records []event
	}
	tests := []tc{
		{"two records", []event{{Name: "a", Count: 1}, {Name: "b", Count: 2}}},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var buf bytes.Buffer
		enc, err := codec.NewEncoder(codec.JSON, &buf)
		if err != nil {
			t.Fatalf("%s: NewEncoder: %v", tc.name, err)
		}
		for _, e := range tc.records {
			if eerr := enc.Encode(e); eerr != nil {
				t.Fatalf("%s: Encode: %v", tc.name, eerr)
			}
		}
		if cerr := enc.Close(); cerr != nil {
			t.Fatalf("%s: Close: %v", tc.name, cerr)
		}
		dec, derr := codec.NewDecoder(codec.JSON, &buf)
		if derr != nil {
			t.Fatalf("%s: NewDecoder: %v", tc.name, derr)
		}
		var got []event
		for dec.More() {
			var e event
			if err := dec.Decode(&e); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatalf("%s: Decode: %v", tc.name, err)
			}
			got = append(got, e)
		}
		if len(got) != len(tc.records) {
			t.Errorf("%s: decoded %d records, want %d", tc.name, len(got), len(tc.records))
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestFromMIME covers direct matches, parameter-bearing MIME headers, and
// unknown-MIME misses.
func TestFromMIME(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     string
		wantF  codec.Format
		wantOK bool
	}
	tests := []tc{
		{"direct json", "application/json", codec.JSON, true},
		{"charset parameter", "application/json; charset=utf-8", codec.JSON, true},
		{"uppercase header", "APPLICATION/JSON", codec.JSON, true},
		{"trailing whitespace", "application/xml ", codec.XML, true},
		{"multiple params", "application/cbor; boundary=xyz; q=0.9", codec.CBOR, true},
		{"unknown misses", "unknown", "", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, ok := codec.FromMIME(tc.in)
		if ok != tc.wantOK || got != tc.wantF {
			t.Errorf("%s: FromMIME(%q) = (%q, %v) want (%q, %v)",
				tc.name, tc.in, got, ok, tc.wantF, tc.wantOK)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestFromExtension covers extension → Format resolution.
func TestFromExtension(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     string
		wantF  codec.Format
		wantOK bool
	}
	tests := []tc{
		{"xml extension", ".xml", codec.XML, true},
		{"yaml alias", ".yml", codec.YAML, true},
		{"unknown misses", ".zzz", "", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, ok := codec.FromExtension(tc.in)
		if ok != tc.wantOK || got != tc.wantF {
			t.Errorf("%s: FromExtension(%q) = (%q, %v) want (%q, %v)",
				tc.name, tc.in, got, ok, tc.wantF, tc.wantOK)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAvailable asserts every M1 Format is present in Available().
func TestAvailable(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want codec.Format
	}
	tests := []tc{
		{"json", codec.JSON},
		{"ndjson", codec.NDJSON},
		{"xml", codec.XML},
		{"csv", codec.CSV},
		{"asn1-der", codec.ASN1DER},
		{"pem", codec.PEM},
		{"yaml", codec.YAML},
		{"toml", codec.TOML},
		{"cbor", codec.CBOR},
		{"msgpack", codec.MsgPack},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		list := codec.Available()
		if !slices.Contains(list, tc.want) {
			t.Errorf("%s: Available() missing %q (got %v)", tc.name, tc.want, list)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HYPER-comprehensive round-trip suite — codec acceptance gate.
//
// The Complex / Inner payload below is intentionally exhaustive: it exercises
// every reasonable serialisation concern (primitive widths, Unicode strings,
// nested structs, slices, maps, time.Time, edge-case numerics, byte blobs,
// nil pointers). Each registered codec round-trips a payload tailored to its
// own contract — the dispatch table (codecAdapter map) below documents every
// quirk so future codec additions slot in by adding one entry.
//
// Discovery is dynamic via codec.Available() — codecs land in the registry
// when their service package init runs (driven by the blank imports in
// codec.go), so additions like TLV and FlatBuffers will be picked up
// automatically once they ship.
// ─────────────────────────────────────────────────────────────────────────────

// innerRT is the nested struct embedded inside complexRT. The tag matrix
// covers json / xml / yaml / toml / cbor / msgpack so every library that
// honours struct tags resolves the field name the same way.
type innerRT struct {
	// Name exercises plain ASCII string handling at depth.
	Name string `json:"name" xml:"name" yaml:"name" toml:"name" cbor:"name" msgpack:"name"`
	// Score exercises float64 round-tripping inside nested records.
	Score float64 `json:"score" xml:"score" yaml:"score" toml:"score" cbor:"score" msgpack:"score"`
	// Tags exercises slice-of-string nesting; xml uses the "tags>tag"
	// element-pair syntax to round-trip cleanly.
	Tags []string `json:"tags" xml:"tags>tag" yaml:"tags" toml:"tags" cbor:"tags" msgpack:"tags"`
	// Extras exercises map[string]string round-tripping at depth. XML cannot
	// natively encode maps so the field is tagged "-".
	Extras map[string]string `json:"extras" xml:"-" yaml:"extras" toml:"extras" cbor:"extras" msgpack:"extras"`
}

// complexRT is the HYPER-comprehensive payload exercised against every
// codec capable of carrying it. Field groups are grouped by concern;
// codec-specific quirks are handled by tweakForCodec or by swapping in a
// specialised payload (CSV / ASN.1 / PEM / FlatBuffers) via codecAdapter.
type complexRT struct {
	// Bool toggles the boolean type.
	Bool bool `json:"bool" xml:"bool" yaml:"bool" toml:"bool" cbor:"bool" msgpack:"bool"`

	//: signed-integer width matrix.
	// Int8 covers the 8-bit signed integer range.
	Int8 int8 `json:"int8" xml:"int8" yaml:"int8" toml:"int8" cbor:"int8" msgpack:"int8"`
	// Int16 covers the 16-bit signed integer range.
	Int16 int16 `json:"int16" xml:"int16" yaml:"int16" toml:"int16" cbor:"int16" msgpack:"int16"`
	// Int32 covers the 32-bit signed integer range.
	Int32 int32 `json:"int32" xml:"int32" yaml:"int32" toml:"int32" cbor:"int32" msgpack:"int32"`
	// Int64 covers the 64-bit signed integer range.
	Int64 int64 `json:"int64" xml:"int64" yaml:"int64" toml:"int64" cbor:"int64" msgpack:"int64"`

	//: unsigned-integer width matrix.
	// Uint8 covers the 8-bit unsigned integer range.
	Uint8 uint8 `json:"uint8" xml:"uint8" yaml:"uint8" toml:"uint8" cbor:"uint8" msgpack:"uint8"`
	// Uint16 covers the 16-bit unsigned integer range.
	Uint16 uint16 `json:"uint16" xml:"uint16" yaml:"uint16" toml:"uint16" cbor:"uint16" msgpack:"uint16"`
	// Uint32 covers the 32-bit unsigned integer range.
	Uint32 uint32 `json:"uint32" xml:"uint32" yaml:"uint32" toml:"uint32" cbor:"uint32" msgpack:"uint32"`
	// Uint64 stays below MaxInt64 because TOML stores integers as int64.
	Uint64 uint64 `json:"uint64" xml:"uint64" yaml:"uint64" toml:"uint64" cbor:"uint64" msgpack:"uint64"`

	//: floating-point precision matrix.
	// Float32 covers the IEEE-754 single-precision range.
	Float32 float32 `json:"float32" xml:"float32" yaml:"float32" toml:"float32" cbor:"float32" msgpack:"float32"`
	// Float64 covers the IEEE-754 double-precision range.
	Float64 float64 `json:"float64" xml:"float64" yaml:"float64" toml:"float64" cbor:"float64" msgpack:"float64"`

	//: string variants — ASCII / multibyte / empty.
	// Plain covers a vanilla ASCII string with no escapes.
	Plain string `json:"plain" xml:"plain" yaml:"plain" toml:"plain" cbor:"plain" msgpack:"plain"`
	// Unicode covers BMP + supplementary planes + RTL.
	Unicode string `json:"unicode" xml:"unicode" yaml:"unicode" toml:"unicode" cbor:"unicode" msgpack:"unicode"`
	// Empty covers the zero-length string distinct from "missing".
	Empty string `json:"empty" xml:"empty" yaml:"empty" toml:"empty" cbor:"empty" msgpack:"empty"`

	//: byte-blob slices. XML stringifies []byte so the field is tagged "-".
	// Binary covers a non-empty []byte payload.
	Binary []byte `json:"binary" xml:"-" yaml:"binary" toml:"binary" cbor:"binary" msgpack:"binary"`

	//: homogeneous slices of various element types.
	// Ints covers a slice of signed integers.
	Ints []int `json:"ints" xml:"ints>i" yaml:"ints" toml:"ints" cbor:"ints" msgpack:"ints"`
	// Strings covers a slice of strings.
	Strings []string `json:"strings" xml:"strings>s" yaml:"strings" toml:"strings" cbor:"strings" msgpack:"strings"`
	// Floats covers a slice of float64.
	Floats []float64 `json:"floats" xml:"floats>f" yaml:"floats" toml:"floats" cbor:"floats" msgpack:"floats"`

	//: maps with string keys (lowest common denominator across codecs);
	//: XML cannot encode maps so these are tagged "-".
	// StringMap covers a map[string]int.
	StringMap map[string]int `json:"stringMap" xml:"-" yaml:"stringMap" toml:"stringMap" cbor:"stringMap" msgpack:"stringMap"`
	// StringStruct covers a map of nested struct values.
	StringStruct map[string]innerRT `json:"stringStruct" xml:"-" yaml:"stringStruct" toml:"stringStruct" cbor:"stringStruct" msgpack:"stringStruct"`

	//: composite — nested struct, populated pointer, nil pointer.
	// Inner is a directly embedded nested struct.
	Inner innerRT `json:"inner" xml:"inner" yaml:"inner" toml:"inner" cbor:"inner" msgpack:"inner"`
	// PInner is a non-nil pointer to a nested struct.
	PInner *innerRT `json:"pInner" xml:"pInner" yaml:"pInner" toml:"pInner" cbor:"pInner" msgpack:"pInner"`
	// Children is a slice of nested structs.
	Children []innerRT `json:"children" xml:"children>child" yaml:"children" toml:"children" cbor:"children" msgpack:"children"`

	//: time round-trip — fixed UTC timestamp; CBOR/MsgPack restore to local TZ
	//: so the equality helper compares via time.Time.Equal.
	// Timestamp covers time.Time across every codec that supports it.
	Timestamp time.Time `json:"timestamp" xml:"timestamp" yaml:"timestamp" toml:"timestamp" cbor:"timestamp" msgpack:"timestamp"`

	//: numeric edge cases.
	// MaxInt64 is math.MaxInt64.
	MaxInt64 int64 `json:"maxInt64" xml:"maxInt64" yaml:"maxInt64" toml:"maxInt64" cbor:"maxInt64" msgpack:"maxInt64"`
	// MinInt64 is math.MinInt64.
	MinInt64 int64 `json:"minInt64" xml:"minInt64" yaml:"minInt64" toml:"minInt64" cbor:"minInt64" msgpack:"minInt64"`
	// NegFloat is a sub-zero float64.
	NegFloat float64 `json:"negFloat" xml:"negFloat" yaml:"negFloat" toml:"negFloat" cbor:"negFloat" msgpack:"negFloat"`
	// LongString covers a multi-kilobyte UTF-8 payload.
	LongString string `json:"longString" xml:"longString" yaml:"longString" toml:"longString" cbor:"longString" msgpack:"longString"`
}

// fixedTimestamp is the deterministic timestamp embedded in every sample
// fixture — fixed in UTC so the equality helper can compare against the
// codec-restored value via time.Time.Equal.
var fixedTimestamp = time.Date(2024, time.January, 15, 9, 30, 0, 0, time.UTC)

// sampleComplex returns the canonical fully-populated complexRT fixture.
// Per-codec subtests start from this value and apply tweakForCodec rewrites.
func sampleComplex() complexRT {
	//: keep deterministic — no time.Now, no rand.
	return complexRT{
		Bool:    true,
		Int8:    -8,
		Int16:   -1600,
		Int32:   -32_000_000,
		Int64:   -64_000_000_000,
		Uint8:   200,
		Uint16:  60_000,
		Uint32:  4_000_000_000,
		Uint64:  9_000_000_000,
		Float32: 3.1415927,
		Float64: 2.718281828459045,

		Plain:   "the quick brown fox jumps over the lazy dog",
		Unicode: "héllo 世界 🌸 \u200fעברית",
		Empty:   "",

		Binary: []byte{0x00, 0x01, 0x02, 0xfe, 0xff},

		Ints:    []int{0, 1, 2, 3, 5, 8, 13, 21, 34},
		Strings: []string{"alpha", "beta", "gamma", "δelta"},
		Floats:  []float64{0, -1.5, 1e10, 1e-10},

		StringMap: map[string]int{
			"one":   1,
			"two":   2,
			"three": 3,
		},
		StringStruct: map[string]innerRT{
			"alpha": {Name: "alpha", Score: 1.5, Tags: []string{"x"}, Extras: map[string]string{"k": "v"}},
			"beta":  {Name: "beta", Score: 2.5, Tags: []string{"y", "z"}, Extras: map[string]string{"k2": "v2"}},
		},

		Inner: innerRT{
			Name:   "root-inner",
			Score:  42.42,
			Tags:   []string{"red", "green", "blue"},
			Extras: map[string]string{"env": "test", "tier": "gold"},
		},
		PInner: &innerRT{
			Name:   "pinner",
			Score:  99.99,
			Tags:   []string{"p1", "p2"},
			Extras: map[string]string{"ptr": "yes"},
		},
		Children: []innerRT{
			{Name: "first", Score: 1.0, Tags: []string{"c1"}, Extras: map[string]string{"i": "0"}},
			{Name: "second", Score: 2.0, Tags: []string{"c2", "c3"}, Extras: map[string]string{"i": "1"}},
		},

		Timestamp: fixedTimestamp,

		MaxInt64:   math.MaxInt64,
		MinInt64:   math.MinInt64,
		NegFloat:   -1.23e-4,
		LongString: strings.Repeat("Lorem ipsum dolor sit amet. ", 256),
	}
}

// nextInt returns a copy of c with scalar integer fields offset by +1.
// Used by streaming and NDJSON subtests that need a small batch of
// distinct records derived from sampleComplex.
func nextInt(c complexRT) complexRT {
	//: shallow copy — we only mutate scalars.
	cp := c
	cp.Int8++
	cp.Int16++
	cp.Int32++
	cp.Int64++
	cp.Uint8++
	cp.Uint16++
	cp.Uint32++
	cp.Uint64++
	//: hand back the offset copy.
	return cp
}

// xmlDoc is the specialised XML payload — wraps the XML-safe subset of
// complexRT plus an XMLName field so reflect.DeepEqual matches after decode.
// Fields that cannot round-trip through encoding/xml (maps, raw []byte)
// are intentionally omitted.
type xmlDoc struct {
	// XMLName binds the wrapper element so encoder + decoder agree.
	XMLName stdxml.Name `xml:"doc"`
	// Bool toggles the boolean type.
	Bool bool `xml:"bool"`
	// Int64 covers signed integer width.
	Int64 int64 `xml:"int64"`
	// Plain covers vanilla ASCII strings.
	Plain string `xml:"plain"`
	// Unicode covers multibyte / RTL strings.
	Unicode string `xml:"unicode"`
	// Ints covers a slice of signed integers via the > element-pair syntax.
	Ints []int `xml:"ints>i"`
	// Strings covers a slice of strings.
	Strings []string `xml:"strings>s"`
	// Inner exercises nested struct encoding.
	Inner innerRT `xml:"inner"`
	// Children exercises a slice of nested structs.
	Children []innerRT `xml:"children>child"`
	// Timestamp covers RFC3339 time.Time encoding.
	Timestamp time.Time `xml:"timestamp"`
}

// sampleXML returns the canonical xmlDoc fixture derived from sampleComplex.
func sampleXML() xmlDoc {
	//: derive every field from the universal sample for consistency.
	s := sampleComplex()
	//: hand back the XML-safe projection.
	return xmlDoc{
		XMLName:   stdxml.Name{Local: "doc"},
		Bool:      s.Bool,
		Int64:     s.Int64,
		Plain:     s.Plain,
		Unicode:   s.Unicode,
		Ints:      s.Ints,
		Strings:   s.Strings,
		Inner:     innerRT{Name: s.Inner.Name, Score: s.Inner.Score, Tags: s.Inner.Tags}, // drop Extras (xml:"-")
		Children:  []innerRT{{Name: s.Children[0].Name, Score: s.Children[0].Score, Tags: s.Children[0].Tags}},
		Timestamp: s.Timestamp,
	}
}

// asn1Doc is the specialised ASN.1 payload. encoding/asn1's strict tag
// rules reject most generic Go shapes, so the structure is intentionally
// flat: only fields whose Go type maps onto a documented ASN.1 type
// (BOOLEAN, INTEGER, OCTET STRING / UTF8STRING, SEQUENCE OF INTEGER).
type asn1Doc struct {
	// Name exercises ASN.1 PRINTABLESTRING / UTF8STRING handling.
	Name string
	// Age exercises ASN.1 INTEGER handling.
	Age int
	// Scores exercises SEQUENCE OF INTEGER handling.
	Scores []int
	// Flag exercises ASN.1 BOOLEAN handling.
	Flag bool
}

// sampleASN1 returns the canonical asn1Doc fixture.
func sampleASN1() asn1Doc {
	//: deterministic — every field has a documented ASN.1 type mapping.
	return asn1Doc{Name: "kitsune", Age: 27, Scores: []int{10, 20, 30}, Flag: true}
}

// samplePEMBlock returns a deterministic *pem.Block used by the PEM
// subtest. PEM is a thin wrapper around a typed block, so a single
// fixture exercises every wire-format dimension the codec touches.
func samplePEMBlock() *stdpem.Block {
	//: encoding/pem.Decode strips trailing whitespace from header values,
	//: so the value is chosen to survive round-trip without normalisation.
	return &stdpem.Block{
		Type:    "TEST CERTIFICATE",
		Headers: map[string]string{"Version": "1", "Encoding": "x-test"},
		Bytes:   []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01, 0x02, 0x03},
	}
}

// sampleCSV returns the canonical CSV fixture: a [][]string matrix that
// exercises empty cells, multibyte content, and several rows.
func sampleCSV() [][]string {
	//: CSV does not preserve types — every cell is a string by design.
	return [][]string{
		{"name", "country", "score"},
		{"alpha", "FR", "42"},
		{"beta", "DE", "7"},
		{"gamma", "世界", "0"},
		{"empty", "", ""},
	}
}

// sampleFlatBuffer returns a deterministic []byte payload that survives
// any passthrough codec — used by the FlatBuffers subtest once that codec
// is registered. The shape (4-byte length prefix + magic + payload)
// keeps the buffer self-identifying for casual inspection.
func sampleFlatBuffer() []byte {
	//: synthesised buffer; not a real flatbuffer schema but the codec
	//: under test is a passthrough so any non-empty []byte will do.
	return []byte{0x00, 0x00, 0x00, 0x10, 'F', 'B', '0', '1', 0xca, 0xfe, 0xba, 0xbe, 0xde, 0xad, 0xbe, 0xef}
}

// tweakForCodec applies codec-specific rewrites that keep sampleComplex
// round-trippable through formats with structural or numeric limitations.
// The returned complexRT is what the assertion compares against.
func tweakForCodec(name string, c complexRT) complexRT {
	//: dispatch on the lower-cased codec name.
	switch strings.ToLower(name) {
	//: TOML round-trips empty maps to nil maps; the universal fixture
	//: keeps every nested map populated so this is presently a no-op,
	//: but the switch arm documents the constraint.
	case "toml":
		//: structural fields are already TOML-safe; nothing to scrub.
		return c
	//: every other supported codec accepts the universal shape.
	default:
		//: no rewrites needed.
		return c
	}
}

// complexEqual is a NaN-safe, time.Time-aware equality helper. CBOR and
// MsgPack restore time.Time with a local timezone (not UTC) so reflect.
// DeepEqual fails even when the instant matches. This helper compares
// timestamps via .Equal and falls back to reflect.DeepEqual elsewhere.
func complexEqual(a, b complexRT) bool {
	//: zero the timestamps and compare them out-of-band via .Equal.
	if !a.Timestamp.Equal(b.Timestamp) {
		//: instants differ — full mismatch.
		return false
	}
	//: copy both values and null the timestamp so DeepEqual works on the rest.
	aa, bb := a, b
	aa.Timestamp = time.Time{}
	bb.Timestamp = time.Time{}
	//: every other field is a plain DeepEqual candidate.
	return reflect.DeepEqual(aa, bb)
}

// codecAdapter is the per-codec contract for the round-trip test. Each
// adapter encodes a codec-tailored payload, decodes it, and asserts the
// round-trip is lossless. The check helper takes the raw encoded bytes
// for adapters (e.g. CSV) whose comparison is shape-specific.
type codecAdapter struct {
	// encode marshals the fixture; returns the encoded bytes.
	encode func() ([]byte, error)
	// decodeAndCheck unmarshals the encoded bytes and asserts equality.
	decodeAndCheck func(t *testing.T, name string, data []byte)
}

// codecAdapters maps each registered Format to its round-trip adapter.
// New codecs only need a new entry here; the dispatch tests below pick
// them up automatically.
func codecAdapters() map[codec.Format]codecAdapter {
	//: build the table per call so the per-codec sample is freshly
	//: allocated (avoids accidental cross-test mutation).
	return map[codec.Format]codecAdapter{
		codec.JSON:    universalAdapter("json"),
		codec.YAML:    universalAdapter("yaml"),
		codec.TOML:    universalAdapter("toml"),
		codec.CBOR:    universalAdapter("cbor"),
		codec.MsgPack: universalAdapter("msgpack"),
		codec.NDJSON:  ndjsonAdapter(),
		codec.XML:     xmlAdapter(),
		codec.CSV:     csvAdapter(),
		codec.ASN1DER: asn1Adapter(),
		codec.PEM:     pemAdapter(),
		//: TLV (when registered) — tlv decodes structs to map[string]any
		//: so we round-trip a primitive payload that survives.
		codec.Format("tlv"): tlvAdapter(),
		//: FlatBuffers (when registered) — passthrough []byte.
		codec.Format("flatbuffers"): flatbuffersAdapter(),
		//: baseenc family — pipeline is JSON-mediated so the universal
		//: complexRT shape round-trips byte-for-byte through every variant.
		codec.Format("base64"):    universalAdapter("base64"),
		codec.Format("base64url"): universalAdapter("base64url"),
		codec.Format("base32"):    universalAdapter("base32"),
		codec.Format("base16"):    universalAdapter("base16"),
		codec.Format("hex"):       universalAdapter("hex"),
		codec.Format("ascii85"):   universalAdapter("ascii85"),
	}
}

// universalAdapter builds the round-trip adapter for codecs that accept
// the complexRT fixture verbatim (json, yaml, toml, cbor, msgpack).
func universalAdapter(name string) codecAdapter {
	//: capture the format name in the closure so encode/decode pick the
	//: right codec from the facade.
	f := codec.Format(name)
	return codecAdapter{
		encode: func() ([]byte, error) {
			//: tweak the sample for codec-specific quirks (Inf/NaN, etc.).
			val := tweakForCodec(name, sampleComplex())
			//: dispatch through the facade.
			return codec.Marshal(f, val)
		},
		decodeAndCheck: func(t *testing.T, name string, data []byte) {
			t.Helper()
			//: decode into a fresh zero value.
			var got complexRT
			if err := codec.Unmarshal(f, data, &got); err != nil {
				//: codec rejected its own output — hard failure.
				t.Fatalf("%s: Unmarshal err=%v", name, err)
			}
			//: build the expected fixture using the same tweak.
			want := tweakForCodec(name, sampleComplex())
			//: compare via the time-aware helper.
			if !complexEqual(got, want) {
				//: surface a compact diff hint.
				t.Errorf("%s: round-trip mismatch\n  got:  %#v\n  want: %#v", name, got, want)
			}
		},
	}
}

// ndjsonAdapter builds the round-trip adapter for the NDJSON codec, which
// expects a slice of values (one record per line).
func ndjsonAdapter() codecAdapter {
	//: closure captures three sequential records derived from sampleComplex.
	records := func() []complexRT {
		//: three records keep the line-count assertion non-trivial.
		base := tweakForCodec("ndjson", sampleComplex())
		return []complexRT{base, nextInt(base), nextInt(nextInt(base))}
	}
	return codecAdapter{
		encode: func() ([]byte, error) {
			//: NDJSON expects a slice value.
			return codec.Marshal(codec.NDJSON, records())
		},
		decodeAndCheck: func(t *testing.T, name string, data []byte) {
			t.Helper()
			//: decode into a fresh slice.
			var got []complexRT
			if err := codec.Unmarshal(codec.NDJSON, data, &got); err != nil {
				//: codec rejected its own output.
				t.Fatalf("%s: Unmarshal err=%v", name, err)
			}
			//: rebuild the expected slice.
			want := records()
			//: element count must match.
			if len(got) != len(want) {
				//: surface the count diff.
				t.Fatalf("%s: decoded %d records, want %d", name, len(got), len(want))
			}
			//: per-element comparison via the time-aware helper.
			for i := range want {
				if !complexEqual(got[i], want[i]) {
					//: surface mismatch at the offending index.
					t.Errorf("%s: record %d mismatch\n  got:  %#v\n  want: %#v", name, i, got[i], want[i])
				}
			}
		},
	}
}

// xmlAdapter builds the round-trip adapter for the XML codec using the
// specialised xmlDoc fixture (encoding/xml cannot encode maps or []byte
// losslessly).
func xmlAdapter() codecAdapter {
	return codecAdapter{
		encode: func() ([]byte, error) {
			//: XML-specific fixture sidesteps the maps/byte-blob limitations.
			return codec.Marshal(codec.XML, sampleXML())
		},
		decodeAndCheck: func(t *testing.T, name string, data []byte) {
			t.Helper()
			//: decode into a fresh zero value of the specialised type.
			var got xmlDoc
			if err := codec.Unmarshal(codec.XML, data, &got); err != nil {
				//: codec rejected its own output.
				t.Fatalf("%s: Unmarshal err=%v", name, err)
			}
			//: build the expected fixture.
			want := sampleXML()
			//: timestamp comparison via Equal — XML restores RFC3339 to UTC
			//: cleanly so DeepEqual normally passes, but keep the helper
			//: consistent with the other adapters.
			if !got.Timestamp.Equal(want.Timestamp) {
				//: instants differ.
				t.Errorf("%s: timestamp mismatch got=%v want=%v", name, got.Timestamp, want.Timestamp)
				return
			}
			//: zero out the timestamps before the structural compare.
			got.Timestamp, want.Timestamp = time.Time{}, time.Time{}
			//: full structural compare on the remainder.
			if !reflect.DeepEqual(got, want) {
				//: surface the diff.
				t.Errorf("%s: round-trip mismatch\n  got:  %#v\n  want: %#v", name, got, want)
			}
		},
	}
}

// csvAdapter builds the round-trip adapter for the CSV codec. CSV models
// a [][]string matrix so the universal fixture cannot apply.
func csvAdapter() codecAdapter {
	return codecAdapter{
		encode: func() ([]byte, error) {
			//: CSV-specific tabular fixture.
			return codec.Marshal(codec.CSV, sampleCSV())
		},
		decodeAndCheck: func(t *testing.T, name string, data []byte) {
			t.Helper()
			//: decode into a fresh [][]string target.
			var got [][]string
			if err := codec.Unmarshal(codec.CSV, data, &got); err != nil {
				//: codec rejected its own output.
				t.Fatalf("%s: Unmarshal err=%v", name, err)
			}
			//: structural equality is sufficient — CSV has no type info.
			if !reflect.DeepEqual(got, sampleCSV()) {
				//: surface the diff.
				t.Errorf("%s: round-trip mismatch\n  got:  %#v\n  want: %#v", name, got, sampleCSV())
			}
		},
	}
}

// asn1Adapter builds the round-trip adapter for the ASN.1 DER codec
// using the specialised asn1Doc fixture (encoding/asn1's tag rules
// reject most generic Go shapes).
func asn1Adapter() codecAdapter {
	return codecAdapter{
		encode: func() ([]byte, error) {
			//: ASN.1-specific fixture — every field type is documented in
			//: encoding/asn1 (BOOLEAN / INTEGER / UTF8STRING / SEQUENCE OF).
			return codec.Marshal(codec.ASN1DER, sampleASN1())
		},
		decodeAndCheck: func(t *testing.T, name string, data []byte) {
			t.Helper()
			//: decode into a fresh zero value.
			var got asn1Doc
			if err := codec.Unmarshal(codec.ASN1DER, data, &got); err != nil {
				//: codec rejected its own output.
				t.Fatalf("%s: Unmarshal err=%v", name, err)
			}
			//: structural equality on the asn1Doc fixture.
			if !reflect.DeepEqual(got, sampleASN1()) {
				//: surface the diff.
				t.Errorf("%s: round-trip mismatch\n  got:  %#v\n  want: %#v", name, got, sampleASN1())
			}
		},
	}
}

// pemAdapter builds the round-trip adapter for the PEM codec, which
// operates on a *pem.Block. PEM has no internal structure beyond the
// typed block, so the fixture and assertion are intentionally narrow.
func pemAdapter() codecAdapter {
	return codecAdapter{
		encode: func() ([]byte, error) {
			//: PEM accepts *pem.Block directly.
			return codec.Marshal(codec.PEM, samplePEMBlock())
		},
		decodeAndCheck: func(t *testing.T, name string, data []byte) {
			t.Helper()
			//: PEM Unmarshal writes through a **pem.Block — supply the
			//: outer pointer slot so the codec can publish the decoded
			//: block.
			var got *stdpem.Block
			if err := codec.Unmarshal(codec.PEM, data, &got); err != nil {
				//: codec rejected its own output.
				t.Fatalf("%s: Unmarshal err=%v", name, err)
			}
			//: build the expected block.
			want := samplePEMBlock()
			//: structural equality on Type / Headers / Bytes.
			if got == nil || got.Type != want.Type ||
				!reflect.DeepEqual(got.Headers, want.Headers) ||
				!bytes.Equal(got.Bytes, want.Bytes) {
				//: surface the diff.
				t.Errorf("%s: round-trip mismatch\n  got:  %#v\n  want: %#v", name, got, want)
			}
		},
	}
}

// tlvAdapter builds the round-trip adapter for the TLV codec. TLV is
// self-describing and reflection-driven; the codec decodes structs into
// map[string]any so the universal fixture cannot use reflect.DeepEqual.
// We round-trip a primitive payload type that TLV restores byte-for-byte.
func tlvAdapter() codecAdapter {
	//: TLV survives scalar Go values verbatim — pick a single int64 so
	//: the assertion is unambiguous.
	want := int64(-64_000_000_000)
	return codecAdapter{
		encode: func() ([]byte, error) {
			//: dispatch through the facade.
			return codec.Marshal(codec.Format("tlv"), want)
		},
		decodeAndCheck: func(t *testing.T, name string, data []byte) {
			t.Helper()
			//: decode into a fresh int64.
			var got int64
			if err := codec.Unmarshal(codec.Format("tlv"), data, &got); err != nil {
				//: codec rejected its own output.
				t.Fatalf("%s: Unmarshal err=%v", name, err)
			}
			//: direct equality on the primitive payload.
			if got != want {
				//: surface the diff.
				t.Errorf("%s: round-trip mismatch got=%d want=%d", name, got, want)
			}
		},
	}
}

// flatbuffersAdapter builds the round-trip adapter for the FlatBuffers
// codec, which is a passthrough []byte sink. The assertion is
// bytes.Equal between the original buffer and the round-tripped one.
func flatbuffersAdapter() codecAdapter {
	return codecAdapter{
		encode: func() ([]byte, error) {
			//: passthrough — encode the deterministic buffer.
			return codec.Marshal(codec.Format("flatbuffers"), sampleFlatBuffer())
		},
		decodeAndCheck: func(t *testing.T, name string, data []byte) {
			t.Helper()
			//: passthrough decode writes through *[]byte.
			var got []byte
			if err := codec.Unmarshal(codec.Format("flatbuffers"), data, &got); err != nil {
				//: codec rejected its own output.
				t.Fatalf("%s: Unmarshal err=%v", name, err)
			}
			//: byte-level equality is the only assertion FlatBuffers makes.
			if !bytes.Equal(got, sampleFlatBuffer()) {
				//: surface the diff.
				t.Errorf("%s: round-trip mismatch\n  got:  %#v\n  want: %#v", name, got, sampleFlatBuffer())
			}
		},
	}
}

// TestRoundTrip_AllCodecs is the codec-suite acceptance gate. For every
// registered Format it discovers via codec.Available(), it marshals a
// HYPER-complex Go value, unmarshals it back, and asserts the round-trip
// is lossless using a per-codec adapter. Codecs that have not yet
// registered (TLV / FlatBuffers pre-merge) skip cleanly via the
// "adapter not registered" branch.
func TestRoundTrip_AllCodecs(t *testing.T) {
	t.Parallel()
	//: assemble the per-codec dispatch table once.
	adapters := codecAdapters()
	//: discover every registered codec via the facade.
	formats := codec.Available()
	//: build the per-format subtest table.
	type tc struct {
		name    string
		format  codec.Format
		adapter codecAdapter
	}
	var tests []tc
	//: walk every discovered Format and bind its adapter.
	for _, f := range formats {
		//: the adapter table is the codec contract — a missing entry
		//: signals a codec we have not yet characterised.
		adapter, ok := adapters[f]
		//: skip with a t.Logf so the suite stays green on unknown additions
		//: while still surfacing the gap.
		if !ok {
			//: capture the format name for the run-loop below.
			tests = append(tests, tc{name: string(f) + " (unmapped)", format: f})
			continue
		}
		//: mapped codec — bind its adapter.
		tests = append(tests, tc{name: string(f), format: f, adapter: adapter})
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: unmapped codec — hard failure. Every codec discovered via
		//: codec.Available() MUST have an entry in codecAdapters(). The
		//: silent-skip path was retired so a fresh codec registration
		//: cannot slip past CI without a characterised round-trip.
		if tc.adapter.encode == nil {
			//: surface as a fatal so the suite breaks the build.
			t.Fatalf("%s: no adapter registered — every codec discovered via codec.Available() MUST have a codecAdapter entry. Add one to codecAdapters() or remove the codec registration.", tc.name)
		}
		//: marshal through the adapter.
		data, err := tc.adapter.encode()
		//: encode failure is fatal — codec cannot serialise its own payload.
		if err != nil {
			//: surface the encode failure verbatim.
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		//: drive the per-codec round-trip assertion.
		tc.adapter.decodeAndCheck(t, tc.name, data)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestStreamingRoundTrip_AllCodecs runs the streaming-encoder + streaming-
// decoder round-trip for every codec that satisfies core/codec.StreamingCodec.
// Each subtest writes three complexRT records through the encoder and reads
// them back through the decoder.
func TestStreamingRoundTrip_AllCodecs(t *testing.T) {
	t.Parallel()
	//: discover every codec.
	formats := codec.Available()
	//: streaming records — derived from sampleComplex via nextInt.
	type tc struct {
		name    string
		format  codec.Format
		records []complexRT
	}
	var tests []tc
	//: walk every Format and filter to streaming-capable codecs via
	//: type-assertion on the core registry. Every StreamingCodec implementer
	//: that accepts the universal complexRT shape participates in this run;
	//: codecs with specialised decode targets (XML/TOML/TLV) are explicitly
	//: excluded below with a comment naming the limitation — they remain
	//: covered by TestRoundTrip_AllCodecs at the non-streaming level.
	for _, f := range formats {
		//: resolve via the core registry to type-assert StreamingCodec.
		c, ok := corecodec.Lookup(f)
		//: codec must exist (Available guarantees this) and must stream.
		if !ok {
			//: defensive — should not happen.
			continue
		}
		//: only StreamingCodec implementations participate.
		if _, streams := c.(corecodec.StreamingCodec); !streams {
			//: skip non-streaming codecs.
			continue
		}
		//: explicit shape-incompatibility skip list. These codecs stream but
		//: decode into a non-complexRT target shape:
		//:   - xml : encoding/xml requires the xmlDoc fixture (maps / []byte
		//:           cannot round-trip through stdlib XML).
		//:   - toml: go-toml/v2 Decoder reads ONE document per input stream;
		//:           multi-record streams are not part of its contract.
		//:   - tlv : decodes structs into map[string]any so reflect-based
		//:           equality on complexRT cannot work without a per-codec
		//:           comparison oracle.
		switch strings.ToLower(string(f)) {
		case "xml", "toml", "tlv":
			//: documented limitation — surface as a skip below in runCase.
			continue
		}
		//: capture three sequential records using the universal shape.
		base := tweakForCodec(string(f), sampleComplex())
		recs := []complexRT{base, nextInt(base), nextInt(nextInt(base))}
		tests = append(tests, tc{name: string(f), format: f, records: recs})
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: shared transport for encoder + decoder.
		var buf bytes.Buffer
		//: open the streaming encoder.
		enc, err := codec.NewEncoder(tc.format, &buf)
		//: hard failure when the codec lied about streaming support.
		if err != nil {
			//: surface the unexpected dispatch failure.
			t.Fatalf("%s: NewEncoder err=%v", tc.name, err)
		}
		//: stream every record through the encoder.
		for i, rec := range tc.records {
			//: surface per-record failures with positional context.
			if eerr := enc.Encode(rec); eerr != nil {
				//: hard failure on encode.
				t.Fatalf("%s: Encode[%d] err=%v", tc.name, i, eerr)
			}
		}
		//: drain the encoder; codecs that batch flush on Close.
		if cerr := enc.Close(); cerr != nil {
			//: hard failure on close.
			t.Fatalf("%s: Close err=%v", tc.name, cerr)
		}
		//: open the streaming decoder against the populated buffer.
		dec, derr := codec.NewDecoder(tc.format, bytes.NewReader(buf.Bytes()))
		//: hard failure on decoder construction.
		if derr != nil {
			//: surface the unexpected dispatch failure.
			t.Fatalf("%s: NewDecoder err=%v", tc.name, derr)
		}
		//: read each record back and compare element-wise.
		for i, want := range tc.records {
			//: fresh decode target per iteration.
			var got complexRT
			//: surface per-record decode failures with positional context.
			if rerr := dec.Decode(&got); rerr != nil {
				//: EOF only acceptable past the last record.
				if errors.Is(rerr, io.EOF) && i == len(tc.records) {
					break
				}
				//: hard failure on decode.
				t.Fatalf("%s: Decode[%d] err=%v", tc.name, i, rerr)
			}
			//: time-aware comparison handles CBOR/MsgPack timezone restore.
			if !complexEqual(got, want) {
				//: surface mismatch at the offending index.
				t.Errorf("%s: stream record %d mismatch\n  got:  %#v\n  want: %#v", tc.name, i, got, want)
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAppendRoundTrip_AllCodecs runs the Appender-extension round-trip for
// every codec that satisfies core/codec.Appender. The encoded payload is
// appended into a caller-supplied dst pre-filled with a known prefix; the
// assertion verifies the prefix is preserved AND the codec's own Unmarshal
// can decode the bytes written past the prefix.
//
// Registry bijection is enforced up-front: the registered Appender set
// (codec.Available() filtered to Appender) and the expectedAppenders map
// must agree key-by-key in both directions. A swapped name or a missing
// registration trips one of the two loops below; a count check would not.
func TestAppendRoundTrip_AllCodecs(t *testing.T) {
	t.Parallel()
	//: Direction 1 (forward): all registered Appenders are expected.
	//: Iterates the package-level registeredAppenders slice and uses
	//: a.Name + map lookup so the KTN-TEST-REGISTRY-BIJECTION analyzer
	//: classifies this as the forward direction.
	expectedSet := map[string]bool{}
	for _, name := range expectedAppenders {
		expectedSet[name] = true
	}
	for _, a := range registeredAppenders {
		if _, ok := expectedSet[a.Name]; !ok {
			t.Errorf("appender %q registered but not expected — add it to expectedAppenders + a switch arm below", a.Name)
		}
	}
	//: Direction 2 (reverse): all expected Appenders are registered.
	//: Iterates the package-level expectedAppenders slice and uses
	//: registeredSet[name] map lookup so the analyzer classifies this
	//: as the reverse direction.
	registeredSet := map[string]bool{}
	for _, a := range registeredAppenders {
		registeredSet[a.Name] = true
	}
	for _, name := range expectedAppenders {
		if !registeredSet[name] {
			t.Errorf("appender %q expected but not registered", name)
		}
	}
	//: prefix that must survive untouched at the head of the buffer.
	prefix := []byte("PREFIX\x00")
	//: per-codec subtest table.
	type tc struct {
		name     string
		format   codec.Format
		appender corecodec.Appender
		value    any
		decode   func(t *testing.T, name string, data []byte)
	}
	var tests []tc
	//: iterate the expected Appender set rather than codec.Available() so
	//: there is no additional registry-walking loop competing with the
	//: bijection above. The bijection guarantees expectedAppenders ≡
	//: registeredAppenders, so iterating either is equivalent for the
	//: round-trip dispatch below.
	for _, name := range expectedAppenders {
		f := codec.Format(name)
		//: resolve via the core registry to type-assert Appender.
		c, ok := corecodec.Lookup(f)
		//: defensive lookup.
		if !ok {
			//: bijection above already reported the gap; skip the
			//: round-trip so the suite reports one failure per missing
			//: registration rather than cascading panics.
			continue
		}
		//: only Appender implementations participate.
		a, supports := c.(corecodec.Appender)
		//: skip non-appender codecs.
		if !supports {
			//: silent skip — bijection above reports the contract violation.
			continue
		}
		//: shape the payload + decode hook per codec. Every Appender
		//: implementer in expected lands in one of the branches below —
		//: the default arm is a t.Fatal so a freshly-added Appender
		//: cannot slip past the suite uncovered.
		switch name {
		//: NDJSON appends a slice of complexRT records (one JSON object per line).
		case "ndjson":
			//: capture the slice payload.
			base := tweakForCodec(string(f), sampleComplex())
			recs := []complexRT{base, nextInt(base)}
			tests = append(tests, tc{
				name:     string(f),
				format:   f,
				appender: a,
				value:    recs,
				decode: func(t *testing.T, name string, data []byte) {
					t.Helper()
					//: decode into a fresh slice.
					var got []complexRT
					if err := codec.Unmarshal(f, data, &got); err != nil {
						//: hard failure on decode.
						t.Fatalf("%s: Unmarshal err=%v", name, err)
					}
					//: element count must match.
					if len(got) != len(recs) {
						//: surface the count diff.
						t.Fatalf("%s: decoded %d records, want %d", name, len(got), len(recs))
					}
					//: per-element comparison.
					for i := range recs {
						if !complexEqual(got[i], recs[i]) {
							//: surface mismatch at the offending index.
							t.Errorf("%s: record %d mismatch", name, i)
						}
					}
				},
			})
		//: TLV decodes structs to map[string]any — round-trip a primitive
		//: payload type so the assertion stays unambiguous.
		case "tlv":
			//: int64 survives TLV byte-for-byte.
			want := int64(-64_000_000_000)
			tests = append(tests, tc{
				name:     string(f),
				format:   f,
				appender: a,
				value:    want,
				decode: func(t *testing.T, name string, data []byte) {
					t.Helper()
					//: decode into a fresh int64.
					var got int64
					if err := codec.Unmarshal(f, data, &got); err != nil {
						//: hard failure on decode.
						t.Fatalf("%s: Unmarshal err=%v", name, err)
					}
					//: direct equality on the primitive payload.
					if got != want {
						//: surface the diff.
						t.Errorf("%s: append round-trip mismatch got=%d want=%d", name, got, want)
					}
				},
			})
		//: FlatBuffers is a passthrough []byte sink — bytes in, bytes out.
		case "flatbuffers":
			//: deterministic synthesised buffer.
			payload := sampleFlatBuffer()
			tests = append(tests, tc{
				name:     string(f),
				format:   f,
				appender: a,
				value:    payload,
				decode: func(t *testing.T, name string, data []byte) {
					t.Helper()
					//: passthrough decode writes through *[]byte.
					var got []byte
					if err := codec.Unmarshal(f, data, &got); err != nil {
						//: hard failure on decode.
						t.Fatalf("%s: Unmarshal err=%v", name, err)
					}
					//: byte-level equality.
					if !bytes.Equal(got, payload) {
						//: surface the diff.
						t.Errorf("%s: append round-trip mismatch", name)
					}
				},
			})
		//: XML appends the specialised xmlDoc fixture (encoding/xml
		//: cannot encode maps or []byte losslessly, so we reuse the
		//: round-trip test's purpose-built shape).
		case "xml":
			//: capture the xmlDoc payload + structural decode hook.
			val := sampleXML()
			tests = append(tests, tc{
				name:     string(f),
				format:   f,
				appender: a,
				value:    val,
				decode: func(t *testing.T, name string, data []byte) {
					t.Helper()
					//: decode into a fresh xmlDoc.
					var got xmlDoc
					if err := codec.Unmarshal(f, data, &got); err != nil {
						//: hard failure on decode.
						t.Fatalf("%s: Unmarshal err=%v", name, err)
					}
					//: timestamp comparison via Equal — XML restores
					//: RFC3339 to UTC cleanly.
					if !got.Timestamp.Equal(val.Timestamp) {
						//: instants differ.
						t.Errorf("%s: timestamp mismatch got=%v want=%v", name, got.Timestamp, val.Timestamp)
						return
					}
					//: zero out the timestamps before the structural compare.
					got.Timestamp, val.Timestamp = time.Time{}, time.Time{}
					//: full structural compare on the remainder.
					if !reflect.DeepEqual(got, val) {
						//: structural diff.
						t.Errorf("%s: append round-trip mismatch", name)
					}
				},
			})
		//: ASN.1 DER round-trips a deterministic asn1Doc fixture (asn1
		//: cannot encode arbitrary structs — only those with stdlib-
		//: supported type tags).
		case "asn1-der":
			val := sampleASN1()
			tests = append(tests, tc{
				name:     string(f),
				format:   f,
				appender: a,
				value:    val,
				decode: func(t *testing.T, name string, data []byte) {
					t.Helper()
					var got asn1Doc
					if err := codec.Unmarshal(f, data, &got); err != nil {
						t.Fatalf("%s: Unmarshal err=%v", name, err)
					}
					if !reflect.DeepEqual(got, val) {
						t.Errorf("%s: append round-trip mismatch", name)
					}
				},
			})
		//: PEM operates on *pem.Block. The round-trip target is
		//: **pem.Block so Unmarshal can populate it.
		case "pem":
			val := samplePEMBlock()
			tests = append(tests, tc{
				name:     string(f),
				format:   f,
				appender: a,
				value:    val,
				decode: func(t *testing.T, name string, data []byte) {
					t.Helper()
					var got *stdpem.Block
					if err := codec.Unmarshal(f, data, &got); err != nil {
						t.Fatalf("%s: Unmarshal err=%v", name, err)
					}
					if !reflect.DeepEqual(got, val) {
						t.Errorf("%s: append round-trip mismatch", name)
					}
				},
			})
		//: CSV operates on [][]string natively.
		case "csv":
			val := sampleCSV()
			tests = append(tests, tc{
				name:     string(f),
				format:   f,
				appender: a,
				value:    val,
				decode: func(t *testing.T, name string, data []byte) {
					t.Helper()
					var got [][]string
					if err := codec.Unmarshal(f, data, &got); err != nil {
						t.Fatalf("%s: Unmarshal err=%v", name, err)
					}
					if !reflect.DeepEqual(got, val) {
						t.Errorf("%s: append round-trip mismatch", name)
					}
				},
			})
		//: Universal-any group: json + yaml + toml + cbor + msgpack +
		//: every baseenc variant (baseenc is JSON-mediated). All accept
		//: complexRT natively without going through the promotion path.
		case "json", "yaml", "toml", "cbor", "msgpack", "base64", "base64url", "base32", "base16", "hex", "ascii85":
			//: capture the codec + fixture + decode hook.
			val := tweakForCodec(string(f), sampleComplex())
			tests = append(tests, tc{
				name:     string(f),
				format:   f,
				appender: a,
				value:    val,
				decode: func(t *testing.T, name string, data []byte) {
					t.Helper()
					//: decode into a fresh complexRT.
					var got complexRT
					if err := codec.Unmarshal(f, data, &got); err != nil {
						//: hard failure on decode.
						t.Fatalf("%s: Unmarshal err=%v", name, err)
					}
					//: time-aware comparison.
					if !complexEqual(got, val) {
						//: surface the diff.
						t.Errorf("%s: append round-trip mismatch", name)
					}
				},
			})
		default:
			//: fresh Appender implementer with no characterised shape —
			//: fail loudly so the suite cannot slide past silently.
			t.Fatalf("%s: Appender implementer has no case in TestAppendRoundTrip_AllCodecs — add a payload + decode hook for this codec", string(f))
		}
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: clone the prefix so the assertion sees the original bytes.
		dst := slices.Clone(prefix)
		//: call Append; it must extend dst with the encoded bytes.
		out, err := tc.appender.Append(dst, tc.value)
		//: hard failure on encode.
		if err != nil {
			//: surface the encode error verbatim.
			t.Fatalf("%s: Append err=%v", tc.name, err)
		}
		//: the prefix must survive untouched at the head of the buffer.
		if !bytes.HasPrefix(out, prefix) {
			//: surface the prefix corruption.
			t.Fatalf("%s: Append clobbered the prefix\n  got:  %q\n  want prefix: %q", tc.name, out, prefix)
		}
		//: the codec's own Unmarshal must accept the bytes past the prefix.
		tc.decode(t, tc.name, out[len(prefix):])
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestCrossCodec_JSONviaCBOR is a bonus "two codecs in series" assertion:
// encode the universal fixture as CBOR, decode into a Go value, re-encode
// as JSON, decode again, then compare to the source. This catches subtle
// width / precision differences in codecs that both claim to be lossless.
func TestCrossCodec_JSONviaCBOR(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{name: "cbor → json → cbor preserves complexRT"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: build the universal fixture; tweak for the strictest codec in
		//: the chain (json rejects NaN/Inf — sample already has neither).
		want := tweakForCodec("json", sampleComplex())
		//: first hop — encode as CBOR.
		cborBytes, err := codec.Marshal(codec.CBOR, want)
		if err != nil {
			//: hard failure on cbor encode.
			t.Fatalf("%s: cbor Marshal err=%v", tc.name, err)
		}
		//: decode the CBOR bytes back into a fresh complexRT.
		var midA complexRT
		if err := codec.Unmarshal(codec.CBOR, cborBytes, &midA); err != nil {
			//: hard failure on cbor decode.
			t.Fatalf("%s: cbor Unmarshal err=%v", tc.name, err)
		}
		//: second hop — re-encode as JSON.
		jsonBytes, err := codec.Marshal(codec.JSON, midA)
		if err != nil {
			//: hard failure on json encode.
			t.Fatalf("%s: json Marshal err=%v", tc.name, err)
		}
		//: decode the JSON bytes back into the final complexRT.
		var got complexRT
		if err := codec.Unmarshal(codec.JSON, jsonBytes, &got); err != nil {
			//: hard failure on json decode.
			t.Fatalf("%s: json Unmarshal err=%v", tc.name, err)
		}
		//: time-aware comparison.
		if !complexEqual(got, want) {
			//: surface the diff hint.
			t.Errorf("%s: cross-codec round-trip mismatch", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestMarshalMany asserts the broadcast variant returns one entry per
// requested Format on the happy path, joins per-format errors via
// errors.Join on partial failure, and leaves no map entry for a Format
// that did not resolve.
// TestMarshalMany covers the broadcast variant via a table: one row
// per behaviour (happy-path full success, partial failure with one
// bad Format, empty formats variadic no-op). Each row asserts the
// returned map's expected keys + whether the joined err must surface
// CodeUnknownFormat.
func TestMarshalMany(t *testing.T) {
	t.Parallel()
	type marshalManyCase struct {
		name           string
		formats        []codec.Format
		wantKeys       []codec.Format
		wantMissingKey codec.Format
		wantUnknown    bool
	}
	tests := []marshalManyCase{
		{
			name:        "happy_path_three_formats",
			formats:     []codec.Format{codec.JSON, codec.CBOR, codec.MsgPack},
			wantKeys:    []codec.Format{codec.JSON, codec.CBOR, codec.MsgPack},
			wantUnknown: false,
		},
		{
			name:           "partial_failure_unknown_format",
			formats:        []codec.Format{codec.JSON, codec.Format("not-a-real-format"), codec.CBOR},
			wantKeys:       []codec.Format{codec.JSON, codec.CBOR},
			wantMissingKey: codec.Format("not-a-real-format"),
			wantUnknown:    true,
		},
		{
			name:        "empty_formats_returns_empty_map",
			formats:     nil,
			wantKeys:    nil,
			wantUnknown: false,
		},
	}
	value := event{Name: "many-probe", Count: 7}
	runCase := func(t *testing.T, tc marshalManyCase) {
		t.Helper()
		out, err := codec.MarshalMany(value, tc.formats...)
		if tc.wantUnknown {
			if err == nil {
				t.Fatalf("%s: expected joined error", tc.name)
			}
			if !errs.HasCode(err, codec.CodeUnknownFormat) {
				t.Errorf("%s: expected CodeUnknownFormat in err, got %v", tc.name, err)
			}
		} else if err != nil {
			t.Fatalf("%s: MarshalMany err=%v want nil", tc.name, err)
		}
		for _, f := range tc.wantKeys {
			if _, ok := out[f]; !ok {
				t.Errorf("%s: missing entry for %s", tc.name, f)
			}
		}
		if tc.wantMissingKey != "" {
			if _, ok := out[tc.wantMissingKey]; ok {
				t.Errorf("%s: unexpected entry for %s", tc.name, tc.wantMissingKey)
			}
		}
		if len(tc.wantKeys) != len(out) {
			t.Errorf("%s: len(out)=%d want %d", tc.name, len(out), len(tc.wantKeys))
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// marshalManyPromoteUser is the ordinary Go struct MarshalMany's
// promotion-path cases broadcast. It is NOT a [][]string, so a
// constrained codec (csv) rejects its native shape and forces
// MarshalMany down the encodeWithPromotion → promoteMarshal fallback
// that the JSON/CBOR/MsgPack happy-path cases never touch.
type marshalManyPromoteUser struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// TestMarshalMany_PromotionPath drives the encodeWithPromotion fallback
// inside MarshalMany: a constrained Format (csv) plus an ordinary struct
// makes the codec reject the native shape, so MarshalMany must promote
// via the JSON bridge and still record the bytes. The chan case proves
// the symmetric failure arm — promotion's json.Marshal step fails, the
// per-format error is joined, and no map entry is recorded for that
// Format. Kept separate from TestMarshalMany because that table fixes a
// single shared value across all formats, whereas the promotion arms
// need a value the constrained codec rejects natively.
func TestMarshalMany_PromotionPath(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		value       any
		formats     []codec.Format
		wantKeys    []codec.Format
		wantMissing []codec.Format
		wantErr     bool
	}
	tests := []tc{
		{
			//: csv rejects the struct natively → promotion encodes it →
			//: bytes recorded under csv. json is the native control.
			name:     "csv_promotion_succeeds",
			value:    marshalManyPromoteUser{Name: "Ada", Age: 36},
			formats:  []codec.Format{codec.JSON, codec.CSV},
			wantKeys: []codec.Format{codec.JSON, codec.CSV},
			wantErr:  false,
		},
		{
			//: a chan never serialises to the JSON intermediate, so csv
			//: promotion fails; the joined error carries it and csv gets
			//: no map entry, while json still fails the same way (chan is
			//: not json-marshalable for either codec) — both missing.
			name:        "csv_promotion_json_error",
			value:       make(chan int),
			formats:     []codec.Format{codec.CSV},
			wantMissing: []codec.Format{codec.CSV},
			wantErr:     true,
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		out, err := codec.MarshalMany(tc.value, tc.formats...)
		//: error expectation gate.
		if tc.wantErr && err == nil {
			t.Fatalf("%s: expected joined error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s: MarshalMany err=%v want nil", tc.name, err)
		}
		//: every expected key must carry bytes.
		for _, f := range tc.wantKeys {
			if _, ok := out[f]; !ok {
				t.Errorf("%s: missing entry for %s", tc.name, f)
			}
		}
		//: every failed Format must be absent from the map.
		for _, f := range tc.wantMissing {
			if _, ok := out[f]; ok {
				t.Errorf("%s: unexpected entry for %s", tc.name, f)
			}
		}
		//: the map cardinality matches exactly the successful keys.
		if len(out) != len(tc.wantKeys) {
			t.Errorf("%s: len(out)=%d want %d", tc.name, len(out), len(tc.wantKeys))
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// universalRoundtripUser is the canonical value used by
// TestUniversalRoundtripAllCodecs to prove the post-promotion contract:
// every Format accepts the SAME ordinary Go struct in Marshal and
// reconstructs it byte-for-byte in Unmarshal, regardless of whether the
// codec is natively any-aware (json, cbor, msgpack, …) or constrained
// (csv, ndjson, pem, flatbuffers, tlv) — the facade promotion path
// closes the gap. Tags reflect the formats that ship native struct-tag
// support; promoted codecs read the json route and ignore them.
type universalRoundtripUser struct {
	Name string `json:"name" cbor:"name" yaml:"name"`
	Age  int    `json:"age"  cbor:"age"  yaml:"age"`
}

// TestUniversalRoundtripAllCodecs pins the post-promotion contract:
// codec.Marshal(F, User) + codec.Unmarshal(F, data, &back) MUST yield
// back == User for every Format the registry knows. Failure here means
// the facade promotion path regressed for at least one codec —
// previously 5/18 codecs rejected this very call shape.
func TestUniversalRoundtripAllCodecs(t *testing.T) {
	t.Parallel()
	//: canonical fixture; struct intentionally small so the binary
	//: codecs stay easy to inspect by eye if a failure prints hex.
	original := universalRoundtripUser{Name: "Ada", Age: 36}
	//: subtest record — `name` doubles as the t.Run label and the
	//: Format key.
	type universalCase struct {
		name   string
		format codec.Format
	}
	//: build the table dynamically from the live registry so a new
	//: codec registration cannot slip past CI without a roundtrip
	//: proof — but materialise it before iteration so the runCase
	//: closure sees a table-driven shape.
	var tests []universalCase
	//: walk every discovered Format and turn it into a case.
	for _, f := range codec.Available() {
		//: capture the format string for the t.Run label.
		tests = append(tests, universalCase{name: string(f), format: f})
	}
	//: per-case driver isolated so each t.Run body stays a one-line
	//: dispatch.
	runCase := func(t *testing.T, tc universalCase) {
		t.Helper()
		//: encode the user via the facade — fast or promotion path.
		data, mErr := codec.Marshal(tc.format, original)
		//: every codec must accept the ordinary struct.
		if mErr != nil {
			//: surface the failure with the format name for triage.
			t.Fatalf("%s: Marshal err=%v", tc.name, mErr)
		}
		//: decode back into a fresh value of the same Go type.
		var back universalRoundtripUser
		//: feed the bytes back through the facade — symmetric path.
		if uErr := codec.Unmarshal(tc.format, data, &back); uErr != nil {
			//: surface the decode failure with the format name.
			t.Fatalf("%s: Unmarshal err=%v (bytes=% x)", tc.name, uErr, data)
		}
		//: full-field equality is the contract — partial fills count
		//: as failures so the test catches silent drift.
		if back != original {
			//: surface the actual decoded value for triage.
			t.Errorf("%s: roundtrip mismatch: got %+v want %+v (bytes=% x)", tc.name, back, original, data)
		}
	}
	//: every registered Format runs in parallel; t.Parallel is cheap
	//: and surfaces concurrency issues if a codec leaks state.
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestMain is the bench/test harness entrypoint. It enables runtime
// profile-rate hooks only when the matching test flag is set so
// `go test -bench=. -blockprofile=block.out` records contention without
// paying the per-event cost on normal runs. Block + mutex rates both
// default to OFF in Go's runtime, so unset flags leave zero overhead.
//
// Flag names follow the testing package convention: `-blockprofile` is
// exposed as `test.blockprofile`, `-mutexprofile` as `test.mutexprofile`;
// flag.Lookup is the documented way to inspect them from a test binary.
// TestMain lives here rather than in a standalone main_test.go because
// KTN-TEST-FILES requires every test file to map to a source file, and
// not in codec_bench_test.go because KTN-TEST-SUFFIX forbids non-Benchmark
// functions in a _bench_test.go file.
func TestMain(m *testing.M) {
	//: enable block profile only when -blockprofile=... is set.
	//: SetBlockProfileRate(1) records every contention event; leaving it
	//: at the default (0) keeps the runtime overhead nil.
	if f := flag.Lookup("test.blockprofile"); f != nil && f.Value.String() != "" {
		//: rate=1 captures every blocking event — bench introspection only.
		runtime.SetBlockProfileRate(1)
	}
	//: enable mutex profile only when -mutexprofile=... is set.
	if f := flag.Lookup("test.mutexprofile"); f != nil && f.Value.String() != "" {
		//: fraction=1 samples every mutex-contention event (1/rate=1.0).
		runtime.SetMutexProfileFraction(1)
	}
	//: run the regular test/bench suite; propagate the harness exit code.
	os.Exit(m.Run())
}
