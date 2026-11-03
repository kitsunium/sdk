package ndjson_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	corendjson "github.com/kitsunium/sdk/internal/core/data/codec/ndjson"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/ndjson"
)

// streamRec is the record shape the stream tests decode.
type streamRec struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// streaming is the codec under test as a StreamingCodec.
func streaming(t *testing.T) corecodec.StreamingCodec {
	t.Helper()
	sc, ok := ndjson.New().(corecodec.StreamingCodec)
	if !ok {
		t.Fatal("ndjson does not implement codec.StreamingCodec")
	}
	return sc
}

// TestStreamDecodesOneRecordPerDecode is #901's acceptance: a three-record
// stream — blank lines between, the last without its newline — decodes one
// record per Decode, More reporting true, true, true, false, and Decode
// returning io.EOF, unwrapped, once the stream is drained. A one-byte reader
// gives the same records.
func TestStreamDecodesOneRecordPerDecode(t *testing.T) {
	t.Parallel()
	const input = "{\"id\":1,\"name\":\"a\"}\n\n  \n{\"id\":2,\"name\":\"b\"}\r\n{\"id\":3,\"name\":\"c\"}"
	for name, r := range map[string]func() io.Reader{
		"whole":    func() io.Reader { return strings.NewReader(input) },
		"one byte": func() io.Reader { return iotest.OneByteReader(strings.NewReader(input)) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dec := streaming(t).NewDecoder(r())
			var got []streamRec
			var mores []bool
			for {
				more := dec.More()
				mores = append(mores, more)
				if !more {
					break
				}
				var rec streamRec
				if err := dec.Decode(&rec); err != nil {
					t.Fatalf("Decode: %v", err)
				}
				got = append(got, rec)
			}
			if len(got) != 3 || got[0].ID != 1 || got[1].Name != "b" || got[2].ID != 3 {
				t.Fatalf("decoded %+v", got)
			}
			if want := []bool{true, true, true, false}; !equalBools(mores, want) {
				t.Fatalf("More reported %v, want %v", mores, want)
			}
			var rec streamRec
			if err := dec.Decode(&rec); !errors.Is(err, io.EOF) || err != io.EOF {
				t.Fatalf("Decode after the last record = %v, want io.EOF", err)
			}
		})
	}
}

// equalBools reports whether a and b hold the same values.
func equalBools(a, b []bool) bool {
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

// countingReader counts the bytes its reader handed out.
type countingReader struct {
	r io.Reader
	n int
}

// Read reads from the wrapped reader and counts.
func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// TestStreamRefusesAnOversizedLine is #901's second acceptance: a line of
// 10 MiB + 1 byte fails typed, SIZE_EXCEEDED, without the stream being read
// past the line's ceiling — and keeps failing.
func TestStreamRefusesAnOversizedLine(t *testing.T) {
	t.Parallel()
	const ceiling = 10 << 20
	line := bytes.Repeat([]byte{'x'}, ceiling+1)
	src := &countingReader{r: io.MultiReader(bytes.NewReader([]byte("{\"id\":1}\n")), bytes.NewReader(line), strings.NewReader("\n{\"id\":2}\n"), bytes.NewReader(make([]byte, 4<<20)))}
	dec := streaming(t).NewDecoder(src)
	var rec streamRec
	if err := dec.Decode(&rec); err != nil || rec.ID != 1 {
		t.Fatalf("first record: %+v, %v", rec, err)
	}
	err := dec.Decode(&rec)
	if !errs.HasCode(err, corendjson.CodeNDJSONSizeExceeded) || !errs.HasReason(err, "SIZE_EXCEEDED") {
		t.Fatalf("oversized line: %v, want SIZE_EXCEEDED (%v)", err, corendjson.CodeNDJSONSizeExceeded)
	}
	if limit := len("{\"id\":1}\n") + ceiling + 64<<10; src.n > limit {
		t.Fatalf("read %d bytes, more than the line's ceiling allows (%d)", src.n, limit)
	}
	if dec.More() {
		t.Fatal("More reported true after the stream failed")
	}
	if again := dec.Decode(&rec); !errs.HasCode(again, corendjson.CodeNDJSONSizeExceeded) {
		t.Fatalf("Decode after the failure = %v, want the same refusal", again)
	}
}

// TestStreamSkipsAMalformedLine pins the line-delimited recovery: a line that
// is not JSON its record can hold fails its Decode with UNMARSHAL_FAILED, and
// the next Decode reads the line after it.
func TestStreamSkipsAMalformedLine(t *testing.T) {
	t.Parallel()
	dec := streaming(t).NewDecoder(strings.NewReader("{\"id\":1}\nnot json\n{\"id\":3}\n"))
	var rec streamRec
	if err := dec.Decode(&rec); err != nil || rec.ID != 1 {
		t.Fatalf("first: %+v, %v", rec, err)
	}
	if err := dec.Decode(&rec); !errs.HasCode(err, corendjson.CodeNDJSONUnmarshalFailed) {
		t.Fatalf("malformed line: %v, want UNMARSHAL_FAILED", err)
	}
	if !dec.More() {
		t.Fatal("More reported false with a record left")
	}
	if err := dec.Decode(&rec); err != nil || rec.ID != 3 {
		t.Fatalf("after the malformed line: %+v, %v", rec, err)
	}
}

// TestStreamEncoderWritesMarshalsLines pins the encoder: one Encode per
// record writes the line Marshal writes for it — the stream equals Marshal of
// the whole slice — and Close leaves the writer to its owner.
func TestStreamEncoderWritesMarshalsLines(t *testing.T) {
	t.Parallel()
	recs := []streamRec{{1, "a<b>"}, {2, " "}, {3, ""}}
	want, err := ndjson.New().Marshal(recs)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var buf bytes.Buffer
	enc := streaming(t).NewEncoder(&buf)
	for _, rec := range recs {
		if err := enc.Encode(rec); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("stream wrote %q, Marshal writes %q", buf.Bytes(), want)
	}
	if err := enc.Encode(make(chan int)); !errs.HasCode(err, corendjson.CodeNDJSONMarshalFailed) {
		t.Fatalf("Encode of a channel = %v, want MARSHAL_FAILED", err)
	}
	if err := streaming(t).NewEncoder(iotest.TruncateWriter(io.Discard, 0)).Encode(recs[0]); err != nil {
		t.Fatalf("a writer that drops bytes without failing: %v", err)
	}
}
