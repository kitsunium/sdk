package xml_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corexml "github.com/kitsunium/sdk/internal/core/data/codec/xml"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/xml"
)

// countingReader counts the bytes it hands over.
type countingReader struct {
	r io.Reader
	n int
}

// Read reads from r and counts.
func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// item is one element of the streams below.
type item struct {
	V string `xml:",chardata"`
}

// bounded returns the codec's decoder over r with ceiling max.
func bounded(t *testing.T, r io.Reader, max int64) codec.Decoder {
	t.Helper()
	bc, ok := xml.New().(codec.BoundedStreamingCodec)
	if !ok {
		t.Fatal("the xml codec does not implement BoundedStreamingCodec")
	}
	dec, err := bc.NewBoundedDecoder(r, max)
	if err != nil {
		t.Fatalf("NewBoundedDecoder: %v", err)
	}
	return dec
}

// TestBoundedDecoderWindowsEveryElement pins #891 on xml: the ceiling bounds
// one element — exactly max bytes pass, max+1 are refused with SIZE_EXCEEDED
// over UNMARSHAL_FAILED's code — a stream of small elements far longer than
// the ceiling is never refused, and a hostile element is refused after max+1
// bytes; io.EOF still ends a stream cleanly.
func TestBoundedDecoderWindowsEveryElement(t *testing.T) {
	t.Parallel()
	const ceiling = 16
	open, closing := "<i>", "</i>"
	exact := open + strings.Repeat("a", ceiling-len(open)-len(closing)) + closing
	var v item
	dec := bounded(t, strings.NewReader(exact), ceiling)
	//: exactly the ceiling, then the end of the stream.
	if err := dec.Decode(&v); err != nil || !errors.Is(dec.Decode(&v), io.EOF) {
		t.Errorf("an element of exactly the ceiling: %v", err)
	}
	err := bounded(t, strings.NewReader(open+strings.Repeat("a", ceiling-len(open)-len(closing)+1)+closing), ceiling).Decode(&v)
	//: one byte past the ceiling.
	if !errs.HasReason(err, "SIZE_EXCEEDED") || !errs.HasCode(err, corexml.CodeXMLSizeExceeded) || !errs.HasCode(err, corexml.CodeXMLUnmarshalFailed) {
		t.Errorf("an element one byte past the ceiling = %v", err)
	}
	dec = bounded(t, strings.NewReader(strings.Repeat("<i>x</i>", 200)), ceiling)
	//: two hundred small elements, 1 600 bytes in all.
	for i := range 200 {
		if err := dec.Decode(&v); err != nil || v.V != "x" {
			t.Fatalf("element %d = %+v, %v", i, v, err)
		}
	}
	src := &countingReader{r: bytes.NewReader([]byte(open + strings.Repeat("a", 1<<20) + closing))}
	dec = bounded(t, src, ceiling)
	//: a hostile element, refused and latched.
	if err := dec.Decode(&v); !errs.HasReason(err, "SIZE_EXCEEDED") || dec.More() || !errs.HasReason(dec.Decode(&v), "SIZE_EXCEEDED") {
		t.Errorf("a megabyte element = %v", err)
	}
	//: after reading no more than the ceiling and one byte.
	if src.n > ceiling+1 {
		t.Errorf("read %d bytes, want at most %d", src.n, ceiling+1)
	}
	bc, _ := xml.New().(codec.BoundedStreamingCodec)
	//: a ceiling of zero or below bounds nothing and is refused.
	if _, err := bc.NewBoundedDecoder(strings.NewReader(""), -1); !errors.Is(err, codec.BoundInvalid) {
		t.Errorf("ceiling -1 = %v, want BOUND_INVALID", err)
	}
}
