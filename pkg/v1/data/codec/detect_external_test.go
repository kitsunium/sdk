package codec_test

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"math/rand/v2"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec"
)

// detectUser is the value every format's fixture below encodes.
type detectUser struct {
	Name string `json:"name" xml:"name" cbor:"name" yaml:"name" toml:"name" msgpack:"name" bson:"name"`
	Age  int    `json:"age" xml:"age" cbor:"age" yaml:"age" toml:"age" msgpack:"age" bson:"age"`
}

// TestDetectRecognisesTheFormatsWithAHead is #909's acceptance: Detect on the
// bytes Marshal wrote returns the Format for every format with a recognisable
// head — JSON, NDJSON of two records, XML, PEM, BSON — and, over every
// registered Format, never answers a wrong one: a format with no head of its
// own is (Format(""), false), and a one-record NDJSON stream is JSON.
func TestDetectRecognisesTheFormatsWithAHead(t *testing.T) {
	t.Parallel()
	user := detectUser{Name: "Ada", Age: 36}
	for _, f := range []codec.Format{codec.JSON, codec.XML, codec.PEM, codec.BSON} {
		data, err := codec.Marshal(f, user)
		if err != nil {
			t.Fatalf("Marshal(%s): %v", f, err)
		}
		//: recognised by its head.
		if got, ok := codec.Detect(data); !ok || got != f {
			t.Errorf("Detect(%s bytes %q) = %q, %v", f, data[:min(len(data), 40)], got, ok)
		}
	}
	records, err := codec.Marshal(codec.NDJSON, []detectUser{user, user})
	if err != nil {
		t.Fatalf("Marshal(ndjson): %v", err)
	}
	//: two records make a stream.
	if got, ok := codec.Detect(records); !ok || got != codec.NDJSON {
		t.Errorf("Detect(two ndjson records) = %q, %v", got, ok)
	}
	for _, f := range codec.Available() {
		data, err := codec.Marshal(f, user)
		if err != nil {
			t.Fatalf("Marshal(%s): %v", f, err)
		}
		got, ok := codec.Detect(data)
		//: one record is a JSON document.
		if f == codec.NDJSON {
			f = codec.JSON
		}
		//: right, or no answer at all.
		if ok && got != f {
			t.Errorf("Detect(%s bytes) = %q, a wrong format", f, got)
		}
	}
}

// TestDetectReadsTheHeadOnly pins the 512-byte window and the refusals: a
// document far longer than the window is recognised from its head, a JSON
// head cut mid-value too, and random bytes, the empty input and the facade's
// own compressed frame are no Format.
func TestDetectReadsTheHeadOnly(t *testing.T) {
	t.Parallel()
	long := append([]byte(`{"payload":"`), bytes.Repeat([]byte("x"), 4096)...)
	//: a document cut by the window.
	if got, ok := codec.Detect(long); !ok || got != codec.JSON {
		t.Errorf("Detect(a 4 KiB JSON head) = %q, %v", got, ok)
	}
	rng := rand.New(rand.NewChaCha8([32]byte{9, 0, 9}))
	buf := make([]byte, 512)
	//: random bytes claim nothing.
	for i := range 1000 {
		for j := range buf {
			buf[j] = byte(rng.Uint32())
		}
		if got, ok := codec.Detect(buf); ok {
			t.Fatalf("Detect(random bytes, round %d) = %q", i, got)
		}
	}
	//: no bytes.
	if got, ok := codec.Detect(nil); ok {
		t.Errorf("Detect(nil) = %q", got)
	}
	for _, algo := range []codec.CompressAlgorithm{codec.Gzip, codec.Flate} {
		frame, err := codec.MarshalCompressed(codec.JSON, algo, detectUser{Name: "Ada"})
		if err != nil {
			t.Fatalf("MarshalCompressed(%s): %v", algo, err)
		}
		//: the frame is UnmarshalCompressed's.
		if got, ok := codec.Detect(frame); ok {
			t.Errorf("Detect(a %s frame) = %q", algo, got)
		}
		if got, ok := codec.DetectCompression(frame); ok {
			t.Errorf("DetectCompression(a %s frame) = %q", algo, got)
		}
	}
}

// TestDetectCompression pins the magic numbers: gzip and zlib as the standard
// library writes them, a Zstandard frame and the S2 and Snappy stream
// identifiers by their bytes; raw DEFLATE and text have none.
func TestDetectCompression(t *testing.T) {
	t.Parallel()
	var gz, zl, fl bytes.Buffer
	gw := gzip.NewWriter(&gz)
	_, _ = gw.Write([]byte("kitsune"))
	_ = gw.Close()
	zw := zlib.NewWriter(&zl)
	_, _ = zw.Write([]byte("kitsune"))
	_ = zw.Close()
	fw, _ := flate.NewWriter(&fl, flate.BestSpeed)
	_, _ = fw.Write([]byte("kitsune"))
	_ = fw.Close()
	for _, tc := range []struct {
		name string
		in   []byte
		want codec.CompressAlgorithm
	}{
		{"gzip", gz.Bytes(), "gzip"},
		{"zlib", zl.Bytes(), "zlib"},
		{"zstd", []byte{0x28, 0xb5, 0x2f, 0xfd, 0x24, 0x07}, "zstd"},
		{"s2", []byte("\xff\x06\x00\x00S2sTwO\x01"), "s2"},
		{"snappy", []byte("\xff\x06\x00\x00sNaPpY\x01"), "s2"},
		{"flate", fl.Bytes(), ""},
		{"text", []byte("kitsune"), ""},
		{"empty", nil, ""},
	} {
		got, ok := codec.DetectCompression(tc.in)
		//: the algorithm, or none.
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("DetectCompression(%s) = %q, %v; want %q", tc.name, got, ok, tc.want)
		}
	}
}

// TestDetectHeads pins each sniffer's edges through Detect: what claims, and
// the near misses that must not.
func TestDetectHeads(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want codec.Format
	}{
		{" \n{\"a\": [1, true, null]}\n", codec.JSON},
		{"[]", codec.JSON},
		{"{\"cut", codec.JSON},
		{"{a: 1}", ""},
		{"[x]", ""},
		{"42", ""},
		{"\"text\"", ""},
		{"{\"a\":1}\n{\"b\":2}\n", codec.NDJSON},
		{"[1]\r\n[2", codec.NDJSON},
		{"{\"a\":1} {\"b\":2}", ""},
		{"\xEF\xBB\xBF<?xml version=\"1.0\"?><a/>", codec.XML},
		{"<root attr=\"1\">", codec.XML},
		{"<!-- c --><a/>", codec.XML},
		{"<1>", ""},
		{"<?xmlfoo", ""},
		{"\n-----BEGIN CERTIFICATE-----\n", codec.PEM},
		{"-----END CERTIFICATE-----", ""},
		{"\x05\x00\x00\x00\x00", codec.BSON},
		{"\x0e\x00\x00\x00\x10age\x00\x24\x00\x00\x00\x00", codec.BSON},
		{"\x0e\x00\x00\x00\x10age\x00\x24\x00\x00\x00\x01", ""},
		{"\x0e\x00\x00\x00\x20age\x00\x24\x00\x00\x00\x00", ""},
		{"\x0e\x00\x00\x00\x10a\x01e\x00\x24\x00\x00\x00\x00", ""},
		{"name: Ada\n", ""},
		{"name = 'Ada'\n", ""},
	} {
		got, ok := codec.Detect([]byte(tc.in))
		//: the format, or none.
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("Detect(%q) = %q, %v; want %q", tc.in, got, ok, tc.want)
		}
	}
}
