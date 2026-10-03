package msgpack_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/msgpack"
)

// streamCap is the stream bound the decoder enforces: 10 MiB, plus one.
const streamCap int = 10<<20 + 1

// failingIO fails every Read and Write with errIO.
type failingIO struct{}

// errIO is the I/O failure failingIO returns.
var errIO = errors.New("io failure")

// Read fails.
func (failingIO) Read([]byte) (int, error) { return 0, errIO }

// Write fails.
func (failingIO) Write([]byte) (int, error) { return 0, errIO }

// countingSource counts the bytes a decoder pulls.
type countingSource struct {
	r    io.Reader
	read int
}

// Read delegates and counts.
func (c *countingSource) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

// streaming returns the codec as a StreamingCodec.
func streaming(t testing.TB) codec.StreamingCodec {
	t.Helper()
	sc, ok := msgpack.New().(codec.StreamingCodec)
	if !ok {
		t.Fatal("codec is not a StreamingCodec")
	}
	return sc
}

// TestStreamRoundTrip writes values with one Encoder and reads them back with
// one Decoder: the stream is the concatenation of Marshal's outputs, More
// holds until the clean end, and io.EOF repeats.
func TestStreamRoundTrip(t *testing.T) {
	t.Parallel()
	sc := streaming(t)
	values := []any{
		map[string]any{"a": []any{int8(1), "x", map[string]any{"deep": []any{}}}},
		"plain",
		[]any{nil, true, 2.5},
	}
	var buf bytes.Buffer
	enc := sc.NewEncoder(&buf)
	var concatenated []byte
	for _, v := range values {
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
		concatenated = append(concatenated, mustMarshal(t, v)...)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), concatenated) {
		t.Fatalf("stream %x differs from Marshal %x", buf.Bytes(), concatenated)
	}
	dec := sc.NewDecoder(&buf)
	for i, want := range values {
		if !dec.More() {
			t.Fatalf("More false before value %d", i)
		}
		var got any
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("value %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("value %d: got %#v, want %#v", i, got, want)
		}
	}
	for range 2 {
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			t.Fatalf("after the last value: %v, want io.EOF", err)
		}
	}
	if dec.More() {
		t.Fatal("More true after io.EOF")
	}
}

// TestStreamFailureIsSticky ends the stream on a value cut short and on a
// value that does not fit, replaying the failure on every later Decode.
func TestStreamFailureIsSticky(t *testing.T) {
	t.Parallel()
	sc := streaming(t)
	tests := []struct {
		name   string
		stream []byte
		target func() any
	}{
		{"cut inside a value", append(mustMarshal(t, 1), 0x92, 0x01), func() any { return new(any) }},
		{"value does not fit", append(mustMarshal(t, 1), mustMarshal(t, "s")...), func() any { return new(int) }},
		{"never-used byte", []byte{0x01, 0xc1}, func() any { return new(any) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dec := sc.NewDecoder(bytes.NewReader(tc.stream))
			if err := dec.Decode(tc.target()); err != nil {
				t.Fatalf("first value: %v", err)
			}
			first := dec.Decode(tc.target())
			if !errs.HasReason(first, "UNMARSHAL_FAILED") || dec.More() {
				t.Fatalf("second value: %v (More=%v)", first, dec.More())
			}
			if again := dec.Decode(tc.target()); again != first {
				t.Fatalf("later Decode returned %v, want the same failure", again)
			}
		})
	}
}

// TestStreamRefusesOversizedLengthBeforeReading declares a bin longer than
// the stream's bound: the decoder refuses at the header, having read only its
// read-ahead, not the body.
func TestStreamRefusesOversizedLengthBeforeReading(t *testing.T) {
	t.Parallel()
	const body uint32 = 11 << 20
	stream := append(binary.BigEndian.AppendUint32([]byte{0xc6}, body), make([]byte, body)...)
	src := &countingSource{r: bytes.NewReader(stream)}
	var got []byte
	err := streaming(t).NewDecoder(src).Decode(&got)
	if !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Fatalf("want UNMARSHAL_FAILED, got %v", err)
	}
	if src.read > 64<<10 {
		t.Fatalf("read %d bytes before refusing a header", src.read)
	}
}

// TestStreamBoundIsTheWholeStream keeps the vendor-era rule: the bound is on
// the whole stream, so a stream of small values longer than it ends there,
// without the decoder ever pulling more than the bound.
func TestStreamBoundIsTheWholeStream(t *testing.T) {
	t.Parallel()
	one := mustMarshal(t, string(make([]byte, 1000)))
	stream := bytes.Repeat(one, streamCap/len(one)+100)
	src := &countingSource{r: bytes.NewReader(stream)}
	dec := streaming(t).NewDecoder(src)
	decoded := 0
	for {
		var s string
		err := dec.Decode(&s)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errs.HasReason(err, "UNMARSHAL_FAILED") {
				t.Fatalf("stream ended with %v", err)
			}
			break
		}
		decoded++
	}
	if src.read > streamCap || decoded*len(one) > streamCap || decoded == 0 {
		t.Fatalf("decoded %d values, read %d bytes, bound %d", decoded, src.read, streamCap)
	}
}

// TestStreamNilAndFailingEnds reports a nil reader or writer, and a failing
// one, as typed failures instead of panics.
func TestStreamNilAndFailingEnds(t *testing.T) {
	t.Parallel()
	sc := streaming(t)
	var v any
	if err := sc.NewDecoder(nil).Decode(&v); !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Fatalf("nil reader: %v", err)
	}
	if err := sc.NewDecoder(failingIO{}).Decode(&v); !errs.HasReason(err, "UNMARSHAL_FAILED") || !errors.Is(err, errIO) {
		t.Fatalf("failing reader: %v", err)
	}
	if err := sc.NewEncoder(nil).Encode(1); !errs.HasReason(err, "MARSHAL_FAILED") {
		t.Fatalf("nil writer: %v", err)
	}
	if err := sc.NewEncoder(failingIO{}).Encode(1); !errs.HasReason(err, "MARSHAL_FAILED") || !errors.Is(err, errIO) {
		t.Fatalf("failing writer: %v", err)
	}
}

// TestStreamFailedEncodeWritesNothing leaves the stream untouched when a
// value fails halfway through encoding.
func TestStreamFailedEncodeWritesNothing(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	enc := streaming(t).NewEncoder(&buf)
	if err := enc.Encode([]any{1, 2, make(chan int)}); !errs.HasReason(err, "MARSHAL_FAILED") {
		t.Fatalf("want MARSHAL_FAILED, got %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("a failed Encode wrote %x", buf.Bytes())
	}
}

// TestStreamAcrossTheReadAhead decodes values of every size around the
// decoder's 4 KiB read-ahead, so some lie wholly in it (decoded in place)
// and others straddle its end (framed piece by piece): both paths must yield
// the values that were written, in order.
func TestStreamAcrossTheReadAhead(t *testing.T) {
	t.Parallel()
	var want []any
	var stream bytes.Buffer
	for size := 0; size < 9000; size += 97 {
		v := map[string]any{"n": int8(size % 100), "s": string(bytes.Repeat([]byte{'a' + byte(size%26)}, size))}
		want = append(want, v)
		stream.Write(mustMarshal(t, v))
	}
	dec := streaming(t).NewDecoder(&stream)
	for i, w := range want {
		var got any
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("value %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, w) {
			t.Fatalf("value %d differs", i)
		}
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last value: %v, want io.EOF", err)
	}
}
