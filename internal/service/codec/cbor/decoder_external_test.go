package cbor_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/cbor"
)

// stuckReader returns no byte and no error, forever.
type stuckReader struct{}

// Read makes no progress.
func (stuckReader) Read([]byte) (int, error) { return 0, nil }

// errRead is the failure failingReader returns after its prefix.
var errRead = errors.New("read failed")

// streaming returns the codec as a StreamingCodec.
func streaming(t *testing.T) codec.StreamingCodec {
	t.Helper()
	sc, ok := cbor.New().(codec.StreamingCodec)
	if !ok {
		t.Fatal("the cbor codec does not stream")
	}
	return sc
}

// decodeAll decodes ints until the decoder stops, returning them and the
// error that stopped it.
func decodeAll(dec codec.Decoder) ([]int, error) {
	var got []int
	for range 64 {
		var n int
		if err := dec.Decode(&n); err != nil {
			return got, err
		}
		got = append(got, n)
	}
	return got, nil
}

// TestDecoder_stream pins the stream decoder: items split across reads,
// a clean end, a truncated end, a reader failure and a stuck reader.
func TestDecoder_stream(t *testing.T) {
	t.Parallel()
	items := mustHex(t, "0118641903e81a000f4240")
	type tc struct {
		name    string
		reader  func() io.Reader
		want    []int
		wantEOF bool
		wantErr string
		cause   error
	}
	tests := []tc{
		{"whole buffer", func() io.Reader { return bytes.NewReader(items) }, []int{1, 100, 1000, 1000000}, true, "", nil},
		{"one byte per read", func() io.Reader { return iotest.OneByteReader(bytes.NewReader(items)) }, []int{1, 100, 1000, 1000000}, true, "", nil},
		{"data with its EOF", func() io.Reader { return iotest.DataErrReader(bytes.NewReader(items)) }, []int{1, 100, 1000, 1000000}, true, "", nil},
		{"empty stream", func() io.Reader { return bytes.NewReader(nil) }, nil, true, "", nil},
		{"truncated item", func() io.Reader { return bytes.NewReader(items[:len(items)-1]) }, []int{1, 100, 1000}, false, "the stream ends inside a data item", nil},
		{"reader failure", func() io.Reader { return io.MultiReader(bytes.NewReader(items[:3]), iotest.ErrReader(errRead)) }, []int{1, 100}, false, "reading the stream failed", errRead},
		{"stuck reader", func() io.Reader { return stuckReader{} }, nil, false, "reading the stream failed", io.ErrNoProgress},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := streaming(t).NewDecoder(tc.reader())
		got, err := decodeAll(dec)
		if !equalInts(got, tc.want) {
			t.Fatalf("%s: decoded %v, want %v", tc.name, got, tc.want)
		}
		if tc.wantEOF {
			if err != io.EOF {
				t.Fatalf("%s: end = %v, want io.EOF unwrapped", tc.name, err)
			}
		} else if !errs.HasReason(err, "UNMARSHAL_FAILED") || !strings.Contains(errs.PrivateOf(err), tc.wantErr) {
			t.Fatalf("%s: end = %v (%s), want %q", tc.name, err, errs.PrivateOf(err), tc.wantErr)
		}
		if tc.cause != nil && !errors.Is(err, tc.cause) {
			t.Errorf("%s: the cause %v is not in the chain of %v", tc.name, tc.cause, err)
		}
		if dec.More() {
			t.Errorf("%s: More reports true after the end", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// equalInts compares two int slices, nil and empty alike.
func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDecoder_recovery pins what follows a failed Decode: an item that does
// not fit the target is consumed and the next one decodes; a malformed item
// ends the stream for good.
func TestDecoder_recovery(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		data      string
		secondErr bool
	}
	tests := []tc{
		{"mismatch then an int", "616102", false},
		{"malformed then anything", "1c02", true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := streaming(t).NewDecoder(bytes.NewReader(mustHex(t, tc.data)))
		var n int
		first := dec.Decode(&n)
		if first == nil {
			t.Fatalf("%s: the first item decoded", tc.name)
		}
		second := dec.Decode(&n)
		if (second != nil) != tc.secondErr {
			t.Fatalf("%s: second Decode err = %v, want failure %v", tc.name, second, tc.secondErr)
		}
		if tc.secondErr && second.Error() != first.Error() {
			t.Errorf("%s: a malformed stream should keep failing the same way: %v then %v", tc.name, first, second)
		}
		if !tc.secondErr && n != 2 {
			t.Errorf("%s: second item = %d, want 2", tc.name, n)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDecoder_largeItem streams an item larger than any read, so the buffer
// grows and the validator resumes many times on one item.
func TestDecoder_largeItem(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		size int
	}
	tests := []tc{{"64 KiB byte string", 64 << 10}, {"300 KiB byte string", 300 << 10}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		payload := bytes.Repeat([]byte{0xab}, tc.size)
		var buf bytes.Buffer
		enc := streaming(t).NewEncoder(&buf)
		if err := enc.Encode(payload); err != nil {
			t.Fatalf("%s: Encode: %v", tc.name, err)
		}
		if err := enc.Encode(7); err != nil {
			t.Fatalf("%s: Encode: %v", tc.name, err)
		}
		dec := streaming(t).NewDecoder(iotest.HalfReader(bytes.NewReader(buf.Bytes())))
		var got []byte
		if err := dec.Decode(&got); err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("%s: Decode = %d bytes, %v", tc.name, len(got), err)
		}
		var n int
		if err := dec.Decode(&n); err != nil || n != 7 {
			t.Fatalf("%s: second item = %d, %v", tc.name, n, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
