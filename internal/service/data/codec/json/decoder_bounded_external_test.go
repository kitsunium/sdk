package json_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corejson "github.com/kitsunium/sdk/internal/core/data/codec/json"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/json"
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

// bounded returns the codec's decoder over doc with ceiling max.
func bounded(t *testing.T, r io.Reader, max int64) codec.Decoder {
	t.Helper()
	bc, ok := json.New().(codec.BoundedStreamingCodec)
	if !ok {
		t.Fatal("the json codec does not implement BoundedStreamingCodec")
	}
	dec, err := bc.NewBoundedDecoder(r, max)
	if err != nil {
		t.Fatalf("NewBoundedDecoder: %v", err)
	}
	return dec
}

// TestBoundedDecoderWindowsEveryValue pins #891 on json: the ceiling bounds
// one value — exactly max bytes pass, max+1 are refused with SIZE_EXCEEDED
// over UNMARSHAL_FAILED's code, a number needing one byte of look-ahead past
// max still passes — a stream of small values far longer than the ceiling is
// never refused, and a hostile value is refused after max+1 bytes.
func TestBoundedDecoderWindowsEveryValue(t *testing.T) {
	t.Parallel()
	const ceiling = 16
	exact := `"` + strings.Repeat("a", ceiling-2) + `"`
	var s string
	//: exactly the ceiling.
	if err := bounded(t, strings.NewReader(exact), ceiling).Decode(&s); err != nil {
		t.Errorf("a value of exactly the ceiling: %v", err)
	}
	var n int64
	//: a number of the ceiling's length, ended by the byte after it.
	if err := bounded(t, strings.NewReader("1234567890123456 "), ceiling).Decode(&n); err != nil || n != 1234567890123456 {
		t.Errorf("a number of the ceiling's length = %d, %v", n, err)
	}
	err := bounded(t, strings.NewReader(`"`+strings.Repeat("a", ceiling-1)+`"`), ceiling).Decode(&s)
	//: one byte past the ceiling.
	if !errs.HasReason(err, "SIZE_EXCEEDED") || !errs.HasCode(err, corejson.CodeJSONSizeExceeded) || !errs.HasCode(err, corejson.CodeJSONUnmarshalFailed) {
		t.Errorf("a value one byte past the ceiling = %v", err)
	}
	dec := bounded(t, strings.NewReader(strings.Repeat(`{"a":1} `, 200)), ceiling)
	//: two hundred small values, 1 600 bytes in all.
	for i := range 200 {
		var v map[string]int
		if err := dec.Decode(&v); err != nil || v["a"] != 1 {
			t.Fatalf("value %d = %v, %v", i, v, err)
		}
	}
	src := &countingReader{r: bytes.NewReader([]byte(`"` + strings.Repeat("a", 1<<20) + `"`))}
	dec = bounded(t, src, ceiling)
	//: a hostile value, refused and latched.
	if err := dec.Decode(&s); !errs.HasReason(err, "SIZE_EXCEEDED") || dec.More() || !errs.HasReason(dec.Decode(&s), "SIZE_EXCEEDED") {
		t.Errorf("a megabyte value = %v", err)
	}
	//: after reading no more than the ceiling and one byte.
	if src.n > ceiling+1 {
		t.Errorf("read %d bytes, want at most %d", src.n, ceiling+1)
	}
	bc, _ := json.New().(codec.BoundedStreamingCodec)
	//: a ceiling of zero or below bounds nothing and is refused.
	if _, err := bc.NewBoundedDecoder(strings.NewReader(""), 0); !errors.Is(err, codec.BoundInvalid) {
		t.Errorf("ceiling 0 = %v, want BOUND_INVALID", err)
	}
}
