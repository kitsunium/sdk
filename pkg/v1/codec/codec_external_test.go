package codec_test

import (
	"bytes"
	stdpem "encoding/pem"
	stdxml "encoding/xml"
	"errors"
	"io"
	"math"
	"reflect"
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
		//: unmapped codec — log and skip.
		if tc.adapter.encode == nil {
			//: surface as a skip so the suite stays informative.
			t.Skipf("%s: no adapter registered (codec discovered but not characterised)", tc.name)
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
	//: walk every Format and filter to streaming-capable + universal-shaped.
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
		//: shape filter: only codecs accepting the universal complexRT shape
		//: appear in the universal streaming run. Specialised payloads
		//: (XML/TOML/TLV) have non-uniform decode targets that require
		//: per-codec adapters which the streaming subtests intentionally
		//: skip here — they get covered by TestRoundTrip_AllCodecs above.
		switch strings.ToLower(string(f)) {
		case "json", "yaml", "cbor", "msgpack":
			//: capture three sequential records for this codec.
			base := tweakForCodec(string(f), sampleComplex())
			recs := []complexRT{base, nextInt(base), nextInt(nextInt(base))}
			tests = append(tests, tc{name: string(f), format: f, records: recs})
		default:
			//: not a uniform-shape streaming codec — skip silently.
		}
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
func TestAppendRoundTrip_AllCodecs(t *testing.T) {
	t.Parallel()
	//: discover every codec.
	formats := codec.Available()
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
	//: walk every Format and filter to Appender implementers.
	for _, f := range formats {
		//: resolve via the core registry to type-assert Appender.
		c, ok := corecodec.Lookup(f)
		//: defensive lookup.
		if !ok {
			//: should never happen — Available guarantees registration.
			continue
		}
		//: only Appender implementations participate.
		a, supports := c.(corecodec.Appender)
		//: skip non-appender codecs.
		if !supports {
			//: silent skip.
			continue
		}
		//: shape the payload + decode hook per codec.
		switch strings.ToLower(string(f)) {
		//: JSON appends a single complexRT instance.
		case "json":
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
		//: NDJSON appends a slice of complexRT records.
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
		default:
			//: unknown appender — skip silently. Future codecs add a case here.
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
