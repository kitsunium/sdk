package codec_test

import (
	"bytes"
	stdxml "encoding/xml"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcmultipart "github.com/kitsunium/sdk/internal/service/data/codec/multipart"
	"github.com/kitsunium/sdk/pkg/v1/data/codec"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/baseenc"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/bson"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/cbor"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/form"
	codecjson "github.com/kitsunium/sdk/pkg/v1/data/codec/json"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/msgpack"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/multipart"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/ndjson"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/tlv"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/toml"
	codecxml "github.com/kitsunium/sdk/pkg/v1/data/codec/xml"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/yaml"
)

// boundCase is one format's input one byte or one level past its bound, and
// the code its refusal must carry.
type boundCase struct {
	format codec.Format
	input  func() []byte
	target func() any
	code   errs.Code
}

// mebibytes10 is the 10 MiB cap most formats share.
const mebibytes10 int = 10 << 20

// filled returns n bytes of b.
func filled(n int, b byte) func() []byte {
	return func() []byte { return bytes.Repeat([]byte{b}, n) }
}

// anyTarget is a fresh untyped target.
func anyTarget() any { return new(any) }

// TestEveryByteCapIsTooLarge is #894's acceptance: for every registered
// Format with a byte cap, Unmarshal of cap+1 bytes is refused with its own
// size code, under the family reason codec.IsTooLarge reads — and still a
// decode failure by the trail. One table over the formats, run one at a time.
func TestEveryByteCapIsTooLarge(t *testing.T) {
	tests := []boundCase{
		{codec.Format(yaml.Format), filled(mebibytes10+1, 'a'), anyTarget, yaml.CodeSizeExceeded},
		{codec.Format(toml.Format), filled(mebibytes10+1, 'a'), anyTarget, toml.CodeSizeExceeded},
		{codec.Format(msgpack.Format), filled(mebibytes10+1, 0xc0), anyTarget, msgpack.CodeSizeExceeded},
		{codec.Format(bson.Format), filled(mebibytes10+1, 0), anyTarget, bson.CodeSizeExceeded},
		{codec.Format(tlv.Format), filled(mebibytes10+1, 0), anyTarget, tlv.CodeSizeExceeded},
		{codec.Format(form.Format), filled(8<<20+1, 'a'), func() any { return new(url.Values) }, form.CodeSizeExceeded},
		{codec.Format(ndjson.Format), filled(mebibytes10+1, '1'), func() any { return new([]any) }, ndjson.CodeSizeExceeded},
		{codec.Format(baseenc.Base64), filled(mebibytes10+1, 'A'), anyTarget, baseenc.CodeSizeExceeded},
		{codec.Format(baseenc.Base58), filled(128<<10+1, '2'), anyTarget, baseenc.CodeSizeExceeded},
	}
	for _, tc := range tests {
		c, ok := corecodec.Lookup(corecodec.Format(tc.format))
		if !ok {
			t.Fatalf("%s is not registered", tc.format)
		}
		err := c.Unmarshal(tc.input(), tc.target())
		if !codec.IsTooLarge(err) || !errs.HasCode(err, tc.code) || codec.IsTooDeep(err) {
			t.Errorf("%s: Unmarshal(cap+1) = %v, want IsTooLarge and code %v", tc.format, err, tc.code)
		}
	}
	limited, err := svcmultipart.NewWithLimits(svcmultipart.LimitsConfig{MaxTotalBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	body := "--b\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\n" + string(bytes.Repeat([]byte{'x'}, 128)) + "\r\n--b--\r\n"
	if err := limited.Unmarshal([]byte(body), new(any)); !codec.IsTooLarge(err) || !errs.HasCode(err, multipart.CodeLimitExceeded) {
		t.Errorf("multipart past MaxTotalBytes = %v, want IsTooLarge through LIMIT_EXCEEDED", err)
	}
}

// TestEveryDepthBoundIsTooDeep is #894 on the depth axis: input nested past
// a format's depth bound is refused with its own depth code, under the
// family reason codec.IsTooDeep reads.
func TestEveryDepthBoundIsTooDeep(t *testing.T) {
	t.Parallel()
	tests := []boundCase{
		{codec.Format(yaml.Format), func() []byte { return bytes.Repeat([]byte{'['}, 101) }, anyTarget, yaml.CodeDepthExceeded},
		{codec.Format(toml.Format), func() []byte { return append([]byte("a = "), bytes.Repeat([]byte{'['}, 1001)...) }, anyTarget, toml.CodeDepthExceeded},
		{codec.Format(msgpack.Format), func() []byte { return append(bytes.Repeat([]byte{0x91}, 1001), 0xc0) }, anyTarget, msgpack.CodeDepthExceeded},
		{codec.Format(cbor.Format), filled(33, 0x81), anyTarget, cbor.CodeDepthExceeded},
	}
	for _, tc := range tests {
		c, ok := corecodec.Lookup(corecodec.Format(tc.format))
		if !ok {
			t.Fatalf("%s is not registered", tc.format)
		}
		err := c.Unmarshal(tc.input(), tc.target())
		if !codec.IsTooDeep(err) || !errs.HasCode(err, tc.code) || codec.IsTooLarge(err) {
			t.Errorf("%s: Unmarshal(past the depth bound) = %v, want IsTooDeep and code %v", tc.format, err, tc.code)
		}
	}
}

// countingReader counts the bytes it hands over.
type countingReader struct {
	r io.Reader
	n int64
}

// Read reads from r and counts.
func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// boundedRecord is the value the bounded-decoder test streams: an XML element
// for xml, a map for json and cbor.
type boundedRecord struct {
	XMLName stdxml.Name `json:"-" cbor:"-" xml:"r"`
	S       string      `json:"s" cbor:"s" xml:"s"`
}

// TestNewBoundedDecoderRefusesPastTheCeiling is #891's acceptance: json, xml
// and cbor decoders built with a ceiling refuse a value twice the ceiling with
// their SIZE_EXCEEDED, which IsTooLarge reads, having read at most ceiling+1
// bytes of the stream; a stream of many small values, longer in all than the
// ceiling, is never refused — the ceiling bounds one value.
func TestNewBoundedDecoderRefusesPastTheCeiling(t *testing.T) {
	t.Parallel()
	const ceiling = 1024
	for _, tc := range []struct {
		format codec.Format
		code   errs.Code
	}{
		{codec.Format(codecjson.Format), codecjson.CodeSizeExceeded},
		{codec.Format(codecxml.Format), codecxml.CodeSizeExceeded},
		{codec.Format(cbor.Format), cbor.CodeSizeExceeded},
	} {
		small, err := codec.Marshal(tc.format, boundedRecord{S: "kitsune"})
		if err != nil {
			t.Fatalf("Marshal(%s): %v", tc.format, err)
		}
		stream := bytes.Repeat(small, 4*ceiling/len(small)+1)
		dec, err := codec.NewBoundedDecoder(tc.format, bytes.NewReader(stream), ceiling)
		if err != nil {
			t.Fatalf("NewBoundedDecoder(%s): %v", tc.format, err)
		}
		//: every small value of a stream four times the ceiling.
		for i := range len(stream) / len(small) {
			var got boundedRecord
			if err := dec.Decode(&got); err != nil || got.S != "kitsune" {
				t.Fatalf("%s: value %d = %+v, %v", tc.format, i, got, err)
			}
		}
		big, err := codec.Marshal(tc.format, boundedRecord{S: strings.Repeat("x", 2*ceiling)})
		if err != nil {
			t.Fatalf("Marshal(%s): %v", tc.format, err)
		}
		src := &countingReader{r: bytes.NewReader(append(append([]byte{}, small...), big...))}
		dec, _ = codec.NewBoundedDecoder(tc.format, src, ceiling)
		var first, got boundedRecord
		//: the small value before it passes.
		if err := dec.Decode(&first); err != nil {
			t.Fatalf("%s: first value: %v", tc.format, err)
		}
		err = dec.Decode(&got)
		//: refused as too large, under the format's own size code.
		if !codec.IsTooLarge(err) || !errs.HasCode(err, tc.code) {
			t.Errorf("%s: Decode of a value twice the ceiling = %v, want %v under SIZE_EXCEEDED", tc.format, err, tc.code)
		}
		//: having read no more than the ceiling past the first value.
		if limit := int64(len(small) + ceiling + 1); src.n > limit {
			t.Errorf("%s: read %d bytes, want at most %d", tc.format, src.n, limit)
		}
		//: the stream stays refused.
		if dec.More() || !codec.IsTooLarge(dec.Decode(&got)) {
			t.Errorf("%s: a refused stream goes on", tc.format)
		}
	}
}

// TestNewBoundedDecoderRefusals pins the three refusals before any byte is
// read: a ceiling of zero or below (ADR 0031), a format whose codec takes no
// ceiling, and an unknown format.
func TestNewBoundedDecoderRefusals(t *testing.T) {
	t.Parallel()
	for _, format := range []codec.Format{codec.Format(codecjson.Format), codec.Format(codecxml.Format), codec.Format(cbor.Format)} {
		for _, ceiling := range []int64{0, -1} {
			_, err := codec.NewBoundedDecoder(format, strings.NewReader("{}"), ceiling)
			//: a bound has no value meaning unlimited.
			if !errors.Is(err, corecodec.BoundInvalid) || !errs.HasReason(err, "BOUND_INVALID") {
				t.Errorf("%s: ceiling %d = %v, want BOUND_INVALID", format, ceiling, err)
			}
		}
	}
	//: toml bounds its own decoder and takes no ceiling.
	if _, err := codec.NewBoundedDecoder(codec.Format(toml.Format), strings.NewReader(""), 1); !errs.HasCode(err, codec.CodeStreamingUnsupported) {
		t.Errorf("toml: %v, want STREAMING_UNSUPPORTED", err)
	}
	//: an unknown format.
	if _, err := codec.NewBoundedDecoder("no-such-format", strings.NewReader(""), 1); !errs.HasCode(err, codec.CodeUnknownFormat) {
		t.Errorf("unknown: %v, want UNKNOWN_FORMAT", err)
	}
}

// TestNewBoundedDecoderRefusesMisuseAndPlacesItsRefusal holds #891's bounded
// decoders to #1101 and #1125: Decode refuses a nil or non-pointer target
// with TARGET_INVALID before reading a byte, so the next Decode still reads
// the first value; and a value past the ceiling is refused at the first
// byte past it — json and xml at the stream's offset, the ceiling counted
// from the end of the previous value; cbor, whose stream positions are its
// item's, at the ceiling within the item.
func TestNewBoundedDecoderRefusesMisuseAndPlacesItsRefusal(t *testing.T) {
	t.Parallel()
	const ceiling = 256
	for _, tc := range []struct {
		format codec.Format
		// at is where the refusal of the second value is placed, given the
		// length of the first.
		at func(first int) int
	}{
		{codec.Format(codecjson.Format), func(first int) int { return first + ceiling }},
		{codec.Format(codecxml.Format), func(first int) int { return first + ceiling }},
		{codec.Format(cbor.Format), func(int) int { return ceiling }},
	} {
		small, err := codec.Marshal(tc.format, boundedRecord{S: "kitsune"})
		if err != nil {
			t.Fatalf("Marshal(%s): %v", tc.format, err)
		}
		big, err := codec.Marshal(tc.format, boundedRecord{S: strings.Repeat("x", 2*ceiling)})
		if err != nil {
			t.Fatalf("Marshal(%s): %v", tc.format, err)
		}
		dec, err := codec.NewBoundedDecoder(tc.format, bytes.NewReader(append(append([]byte{}, small...), big...)), ceiling)
		if err != nil {
			t.Fatalf("NewBoundedDecoder(%s): %v", tc.format, err)
		}
		//: misuse first, before a byte is read.
		for name, target := range map[string]any{"nil": nil, "typed nil": (*boundedRecord)(nil), "non-pointer": boundedRecord{}} {
			if err := dec.Decode(target); !errs.HasCode(err, corecodec.CodeTargetInvalid) || !codec.IsMisuse(err) || codec.IsTooLarge(err) {
				t.Errorf("%s: Decode(%s) = %v, want TARGET_INVALID", tc.format, name, err)
			}
		}
		var first, got boundedRecord
		//: nothing was consumed: the first value is still there.
		if err := dec.Decode(&first); err != nil || first.S != "kitsune" {
			t.Fatalf("%s: first value after the misuse = %+v, %v", tc.format, first, err)
		}
		err = dec.Decode(&got)
		if !codec.IsTooLarge(err) {
			t.Fatalf("%s: Decode past the ceiling = %v, want SIZE_EXCEEDED", tc.format, err)
		}
		//: placed, at the first byte past the ceiling.
		if p, ok := codec.PositionOf(err); !ok || p != (codec.Position{Offset: tc.at(len(small))}) {
			t.Errorf("%s: PositionOf = %+v, %v, want offset %d", tc.format, p, ok, tc.at(len(small)))
		}
		//: a refused stream is refused at the same place.
		if p, _ := codec.PositionOf(dec.Decode(&got)); p.Offset != tc.at(len(small)) {
			t.Errorf("%s: the sticky refusal moved to %+v", tc.format, p)
		}
	}
}
