package cbor_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corecbor "github.com/kitsunium/sdk/internal/core/data/codec/cbor"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/cbor"
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

// textItem is a CBOR text string of n bytes of 'a', n below 256.
func textItem(n int) []byte {
	//: the head: major type 3, the length inline below 24, else in one byte.
	if n < 24 {
		return append([]byte{0x60 | byte(n)}, strings.Repeat("a", n)...)
	}
	return append([]byte{0x78, byte(n)}, strings.Repeat("a", n)...)
}

// TestBoundedDecoderWindowsEveryItem pins #891 on cbor, for the registered,
// strict and deterministic instances: the ceiling bounds one item — exactly
// max bytes pass, max+1 are refused with SIZE_EXCEEDED over UNMARSHAL_FAILED's
// code — a stream of small items far longer than the ceiling is never
// refused, and a hostile item is refused with no more than the ceiling read.
func TestBoundedDecoderWindowsEveryItem(t *testing.T) {
	t.Parallel()
	const ceiling = 16
	for name, c := range map[string]codec.Codec{"registered": cbor.New(), "strict": cbor.NewStrict(), "deterministic": cbor.NewDeterministic()} {
		bc, ok := c.(codec.BoundedStreamingCodec)
		if !ok {
			t.Fatalf("%s: not a BoundedStreamingCodec", name)
		}
		bounded := func(r io.Reader) codec.Decoder {
			dec, err := bc.NewBoundedDecoder(r, ceiling)
			if err != nil {
				t.Fatalf("%s: NewBoundedDecoder: %v", name, err)
			}
			return dec
		}
		var s string
		//: exactly the ceiling: a one-byte head and fifteen bytes.
		if err := bounded(bytes.NewReader(textItem(ceiling - 1))).Decode(&s); err != nil || len(s) != ceiling-1 {
			t.Errorf("%s: an item of exactly the ceiling = %d, %v", name, len(s), err)
		}
		err := bounded(bytes.NewReader(textItem(ceiling - 1 + 1))).Decode(&s)
		//: one byte past the ceiling.
		if !errs.HasReason(err, "SIZE_EXCEEDED") || !errs.HasCode(err, corecbor.CodeCBORSizeExceeded) || !errs.HasCode(err, corecbor.CodeCBORUnmarshalFailed) {
			t.Errorf("%s: an item one byte past the ceiling = %v", name, err)
		}
		dec := bounded(bytes.NewReader(bytes.Repeat([]byte{0x01}, 200)))
		//: two hundred one-byte items.
		for i := range 200 {
			var n int
			if err := dec.Decode(&n); err != nil || n != 1 {
				t.Fatalf("%s: item %d = %d, %v", name, i, n, err)
			}
		}
		//: then a clean end.
		if err := dec.Decode(new(int)); !errors.Is(err, io.EOF) {
			t.Errorf("%s: end = %v, want io.EOF", name, err)
		}
		hostile := append([]byte{0x7a, 0x00, 0x10, 0x00, 0x00}, bytes.Repeat([]byte{'a'}, 1<<20)...)
		src := &countingReader{r: bytes.NewReader(hostile)}
		dec = bounded(src)
		//: a hostile item, refused and latched.
		if err := dec.Decode(&s); !errs.HasReason(err, "SIZE_EXCEEDED") || dec.More() || !errs.HasReason(dec.Decode(&s), "SIZE_EXCEEDED") {
			t.Errorf("%s: a megabyte item = %v", name, err)
		}
		//: after reading no more than the ceiling.
		if src.n > ceiling {
			t.Errorf("%s: read %d bytes, want at most %d", name, src.n, ceiling)
		}
		//: a ceiling of zero or below bounds nothing and is refused.
		if _, err := bc.NewBoundedDecoder(bytes.NewReader(nil), 0); !errors.Is(err, codec.BoundInvalid) {
			t.Errorf("%s: ceiling 0 = %v, want BOUND_INVALID", name, err)
		}
	}
}
