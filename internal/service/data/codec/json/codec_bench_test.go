package json_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/service/data/codec/json"
)

// benchDoc is a record of the shapes an API document holds: scalars, a
// string list, a string map, nested records.
type benchDoc struct {
	ID       int64             `json:"id"`
	Name     string            `json:"name"`
	Score    float64           `json:"score"`
	Active   bool              `json:"active"`
	Tags     []string          `json:"tags"`
	Attrs    map[string]string `json:"attrs"`
	Children []benchDoc        `json:"children,omitempty"`
}

// benchSizes are the documents measured: small (one record), medium (ten
// children), large (two hundred children with long names).
func benchSizes() []struct {
	name string
	doc  *benchDoc
} {
	mk := func(n, nameLen int) *benchDoc {
		d := &benchDoc{ID: 1, Name: "root", Score: 4.5, Active: true, Tags: []string{"a", "b"}, Attrs: map[string]string{"k": "v", "env": "prod"}}
		for i := range n {
			d.Children = append(d.Children, benchDoc{ID: int64(i), Name: strings.Repeat("n", nameLen) + strconv.Itoa(i), Score: float64(i) / 3, Tags: []string{"t"}, Attrs: map[string]string{"i": strconv.Itoa(i)}})
		}
		return d
	}
	return []struct {
		name string
		doc  *benchDoc
	}{{"small", mk(0, 0)}, {"medium", mk(10, 8)}, {"large", mk(200, 64)}}
}

// BenchmarkMarshal encodes each document into a fresh slice.
func BenchmarkMarshal(b *testing.B) {
	c := json.New()
	for _, s := range benchSizes() {
		var v any = s.doc
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				out, err := c.Marshal(v)
				if err != nil {
					b.Fatal(err)
				}
				benchSink = out
			}
		})
	}
}

// BenchmarkAppend encodes each document onto a reused buffer.
func BenchmarkAppend(b *testing.B) {
	a, _ := json.New().(corecodec.Appender)
	for _, s := range benchSizes() {
		var v any = s.doc
		dst := make([]byte, 0, 64<<10)
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				out, err := a.Append(dst[:0], v)
				if err != nil {
					b.Fatal(err)
				}
				benchSink = out
			}
		})
	}
}

// BenchmarkUnmarshal decodes each document into a fresh record.
func BenchmarkUnmarshal(b *testing.B) {
	c := json.New()
	for _, s := range benchSizes() {
		data, _ := c.Marshal(s.doc)
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var d benchDoc
				if err := c.Unmarshal(data, &d); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkStreamEncode writes each document through the stream encoder.
func BenchmarkStreamEncode(b *testing.B) {
	sc, _ := json.New().(corecodec.StreamingCodec)
	for _, s := range benchSizes() {
		var buf bytes.Buffer
		enc := sc.NewEncoder(&buf)
		var v any = s.doc
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				buf.Reset()
				if err := enc.Encode(v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkStreamDecode reads each document through a fresh stream decoder.
func BenchmarkStreamDecode(b *testing.B) {
	sc, _ := json.New().(corecodec.StreamingCodec)
	for _, s := range benchSizes() {
		data, _ := json.New().Marshal(s.doc)
		r := bytes.NewReader(nil)
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				r.Reset(data)
				var d benchDoc
				if err := sc.NewDecoder(r).Decode(&d); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
