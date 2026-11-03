package bson_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corebson "github.com/kitsunium/sdk/internal/core/data/codec/bson"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/bson"
)

// streamDoc is one document of a stream.
type streamDoc struct {
	N    int    `bson:"n"`
	Name string `bson:"name"`
}

// TestStreamRoundTrip is #1100: a mongodump-style file or a socket of
// concatenated documents — the format's own stream layout — had no
// codec.NewDecoder, although the int32 length prefix makes the framing
// trivial. Three documents encode to the concatenation of their Marshal
// outputs and decode back, More reporting false after the third.
func TestStreamRoundTrip(t *testing.T) {
	t.Parallel()
	sc, ok := bson.New().(codec.StreamingCodec)
	if !ok {
		t.Fatal("bson is not a StreamingCodec")
	}
	docs := []streamDoc{{1, "a"}, {2, "bb"}, {3, "ccc"}}
	var buf bytes.Buffer
	enc := sc.NewEncoder(&buf)
	var want []byte
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			t.Fatalf("Encode: %v", err)
		}
		one, err := bson.New().Marshal(d)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		want = append(want, one...)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("the stream is not the concatenation of the three documents")
	}
	if err := enc.Encode(42); !errs.HasReason(err, "BSON_MARSHAL_FAILED") || buf.Len() != len(want) {
		t.Fatalf("a refused Encode must write nothing: %v, %d bytes", err, buf.Len()-len(want))
	}
	dec := sc.NewDecoder(bytes.NewReader(buf.Bytes()))
	for i, d := range docs {
		if !dec.More() {
			t.Fatalf("More false before document %d", i)
		}
		var got streamDoc
		if err := dec.Decode(&got); err != nil || got != d {
			t.Fatalf("document %d: %+v (%v)", i, got, err)
		}
	}
	if dec.More() {
		t.Fatal("More true after the third document")
	}
	var extra streamDoc
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("Decode past the end: want io.EOF, got %v", err)
	}
}

// TestStreamRefusesAnOversizedPrefix declares maxBSONBytes+1: the decoder
// refuses it with SIZE_EXCEEDED before reserving anything of that size.
// No t.Parallel: it reads the process-wide allocation counter.
func TestStreamRefusesAnOversizedPrefix(t *testing.T) {
	sc := bson.New().(codec.StreamingCodec)
	prefix := binary.LittleEndian.AppendUint32(nil, 10<<20+1)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var got streamDoc
	err := sc.NewDecoder(bytes.NewReader(append(prefix, make([]byte, 64)...))).Decode(&got)
	runtime.ReadMemStats(&after)
	if !errs.HasCode(err, corebson.CodeBSONSizeExceeded) {
		t.Fatalf("want SIZE_EXCEEDED, got %v", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Fatalf("allocated %d bytes before refusing", grew)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"a prefix below the minimum document", binary.LittleEndian.AppendUint32(nil, 4)},
		{"a prefix cut short", []byte{0x10, 0x00}},
		{"a document cut short", append(binary.LittleEndian.AppendUint32(nil, 64), 0x10)},
		{"a declared length the reader never delivers", binary.LittleEndian.AppendUint32(nil, 10<<20)},
	} {
		var v streamDoc
		if err := sc.NewDecoder(bytes.NewReader(tc.data)).Decode(&v); !errs.HasReason(err, "BSON_UNMARSHAL_FAILED") {
			t.Errorf("%s: want BSON_UNMARSHAL_FAILED, got %v", tc.name, err)
		}
	}
}
