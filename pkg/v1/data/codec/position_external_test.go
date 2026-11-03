package codec_test

import (
	"slices"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/bson"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/cbor"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/csv"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/flatbuffers"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/json"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/msgpack"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/tlv"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/toml"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/xml"
	_ "github.com/kitsunium/sdk/pkg/v1/data/codec/yaml"
)

// positionCase is one format's malformed or truncated input, the target it
// decodes into, and the position its refusal must carry.
type positionCase struct {
	format codec.Format
	input  []byte
	target func() any
	want   codec.Position
}

// xmlRow is an xml target with one child element.
type xmlRow struct {
	B string `xml:"b"`
}

// tlvRow is a typed tlv target, decoded by the typed path.
type tlvRow struct {
	Name string
	Age  int
}

// positionCases places one refusal per format that knows a position: text
// formats by line and column (json by offset, xml by line), binary formats by
// offset alone — bson's at byte 0, which a binary position means.
var positionCases = []positionCase{
	{codec.YAML, []byte("a: [1, 2\nb: 3\n"), anyTarget, codec.Position{Line: 2, Column: 1}},
	{codec.TOML, []byte("a = 1\nb = = 2\n"), anyTarget, codec.Position{Line: 2, Column: 5}},
	{codec.CSV, []byte("a,b\n\"x,y\n"), func() any { return new([][]string) }, codec.Position{Line: 2, Column: 6}},
	{codec.JSON, []byte(`{"a": 1,}`), anyTarget, codec.Position{Offset: 9}},
	{codec.XML, []byte("<a>\n<b></a>"), func() any { return new(xmlRow) }, codec.Position{Line: 2}},
	{codec.CBOR, []byte{0x82, 0x01}, anyTarget, codec.Position{Offset: 2}},
	{codec.MsgPack, []byte{0x92, 0x01}, anyTarget, codec.Position{Offset: 1}},
	{codec.BSON, []byte{0x10, 0, 0, 0, 0x10, 'a', 0}, anyTarget, codec.Position{Offset: 0}},
	{codec.TLV, []byte{0x60, 0x02, 0x01}, anyTarget, codec.Position{Offset: 2}},
	{codec.FlatBuffers, []byte{1, 2}, func() any { return new([]byte) }, codec.Position{Offset: 2}},
}

// TestEveryRefusalHasAPosition is #1125's acceptance: for every registered
// Format the table names, a malformed or truncated input's refusal carries the
// position the format documents, read back by codec.PositionOf whatever the
// format.
func TestEveryRefusalHasAPosition(t *testing.T) {
	t.Parallel()
	registered := codec.Available()
	for _, tc := range positionCases {
		if !slices.Contains(registered, tc.format) {
			t.Errorf("%s: not registered", tc.format)
			continue
		}
		err := codec.Unmarshal(tc.format, tc.input, tc.target())
		if err == nil {
			t.Errorf("%s: Unmarshal accepted a malformed input", tc.format)
			continue
		}
		got, ok := codec.PositionOf(err)
		if !ok || got != tc.want {
			t.Errorf("%s: PositionOf(%v) = %+v, %v; want %+v, true", tc.format, err, got, ok, tc.want)
		}
	}
}

// TestPositionOfDeeperRefusals places refusals the table's inputs do not
// reach: a csv cell its column cannot hold, a json value its field cannot, a
// tlv record truncated inside a struct decoded by the typed path — placed
// where its declared payload should start, the input's end — and bytes after
// a tlv record.
func TestPositionOfDeeperRefusals(t *testing.T) {
	t.Parallel()
	type priced struct {
		Name  string `csv:"name"`
		Price int    `csv:"price"`
	}
	row, err := codec.Marshal(codec.TLV, tlvRow{Name: "ada", Age: 36})
	if err != nil {
		t.Fatal(err)
	}
	cases := []positionCase{
		{codec.CSV, []byte("name,price\npen,12\nink,abc\n"), func() any { return new([]priced) }, codec.Position{Line: 3, Column: 5}},
		{codec.JSON, []byte(`{"Name": "ada", "Age": "x"}`), func() any { return new(tlvRow) }, codec.Position{Offset: 26}},
		{codec.TLV, row[:len(row)-1], func() any { return new(tlvRow) }, codec.Position{Offset: len(row) - 1}},
		{codec.TLV, append(row[:len(row):len(row)], 0x00), func() any { return new(tlvRow) }, codec.Position{Offset: len(row)}},
	}
	for _, tc := range cases {
		err := codec.Unmarshal(tc.format, tc.input, tc.target())
		got, ok := codec.PositionOf(err)
		if err == nil || !ok || got != tc.want {
			t.Errorf("%s %q: PositionOf(%v) = %+v, %v; want %+v, true", tc.format, tc.input, err, got, ok, tc.want)
		}
	}
}

// TestPositionOfNoPosition is false for a refusal that concerns no byte of
// the input: an unknown format.
func TestPositionOfNoPosition(t *testing.T) {
	t.Parallel()
	err := codec.Unmarshal(codec.Format("no-such-format"), []byte("x"), new(any))
	if p, ok := codec.PositionOf(err); ok {
		t.Errorf("PositionOf(%v) = %+v, true; want false", err, p)
	}
}
