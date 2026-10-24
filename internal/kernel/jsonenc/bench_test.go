package jsonenc_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/jsonenc"
)

// The benchmarks hold AppendString to the two things a caller would otherwise
// use: the byte loop the emitters carried, and encoding/json. A function stays
// only while it wins; BENCH.md has the figures.

// benchSink keeps each result alive.
var benchSink []byte

// loopAppend is the run-copying byte loop the logger encoder carried before
// this package (a per-byte test, one append per run): the control.
func loopAppend(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := range len(s) {
		c := s[i]
		if c >= 0x20 && c != '\\' && c != '"' {
			continue
		}
		dst = append(dst, s[start:i]...)
		switch c {
		case '\\', '"':
			dst = append(dst, '\\', c)
		case '\n':
			dst = append(dst, '\\', 'n')
		default:
			dst = append(dst, '\\', 'u', '0', '0', "0123456789abcdef"[c>>4], "0123456789abcdef"[c&15])
		}
		start = i + 1
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// BenchmarkAppendString runs each implementation over the lengths of a name, a
// message and a body, plain, with escapes, and non-ASCII.
func BenchmarkAppendString(b *testing.B) {
	for _, n := range []int{8, 32, 128, 1024} {
		shapes := []struct{ name, in string }{
			{"plain", strings.Repeat("a", n)},
			{"escaped", strings.Repeat("ab\"d", n/4)},
			{"unicode", strings.Repeat("aé東", n/6+1)},
		}
		for _, sh := range shapes {
			impls := []struct {
				name string
				fn   func(dst []byte, s string) []byte
			}{
				{"jsonenc", jsonenc.AppendString},
				{"loop", loopAppend},
				{"stdlib", func(dst []byte, s string) []byte {
					out, _ := json.Marshal(s)
					return append(dst, out...)
				}},
			}
			for _, im := range impls {
				if im.name == "loop" && sh.name == "unicode" {
					continue // the loop has no UTF-8 validation to compare
				}
				b.Run(sh.name+"/"+strconv.Itoa(n)+"/"+im.name, func(b *testing.B) {
					dst := make([]byte, 0, 4*n+16)
					b.SetBytes(int64(len(sh.in)))
					b.ReportAllocs()
					for b.Loop() {
						benchSink = im.fn(dst[:0], sh.in)
					}
				})
			}
		}
	}
}

// BenchmarkObject prices a five-member object against encoding/json.Marshal of
// the same fields.
func BenchmarkObject(b *testing.B) {
	type doc struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
		N     int64  `json:"n"`
		OK    bool   `json:"ok"`
		ID    uint64 `json:"id"`
	}
	d := doc{"info", "request completed in 12ms", -7, true, 4242}
	b.Run("jsonenc", func(b *testing.B) {
		dst := make([]byte, 0, 256)
		b.ReportAllocs()
		for b.Loop() {
			benchSink = jsonenc.BeginObject(dst[:0]).String("level", d.Level).String("msg", d.Msg).
				Int("n", d.N).Bool("ok", d.OK).Uint("id", d.ID).End()
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchSink, _ = json.Marshal(d)
		}
	})
}
