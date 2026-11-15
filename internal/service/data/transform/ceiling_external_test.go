package transform_test

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctransform "github.com/kitsunium/sdk/internal/service/data/transform"
)

// ceilingPayload is a payload no scheme packs to nothing: words drawn by a
// fixed generator, so a stream cut anywhere decodes to a prefix of it.
func ceilingPayload(size int) []byte {
	words := []string{"kitsune", "fox", "tail", "nine", "shrine", "inari", "lantern", "torii"}
	var buf bytes.Buffer
	state := uint32(1)
	for buf.Len() < size {
		state = state*1664525 + 1013904223
		buf.WriteString(words[(state>>16)%uint32(len(words))])
		buf.WriteByte(byte('a' + (state>>8)%26))
	}
	return buf.Bytes()
}

// ceilingPrefix is what the standard library hands over of a cut stream before
// it reports the cut: the plaintext a decoder has produced when the ceiling
// is judged. ok is false when the cut leaves no stream to read at all.
func ceilingPrefix(name string, cut []byte) (prefix []byte, ok bool) {
	var rd io.Reader
	switch name {
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(cut))
		if err != nil {
			return nil, false
		}
		rd = zr
	case "zlib":
		zr, err := zlib.NewReader(bytes.NewReader(cut))
		if err != nil {
			return nil, false
		}
		rd = zr
	default:
		rd = flate.NewReader(bytes.NewReader(cut))
	}
	prefix, _ = io.ReadAll(rd)
	return prefix, true
}

// Test_Ceiling_TruncatedPastMax holds ADR 0213 to the order of its two
// refusals (kitsunium/sdk#1432): once a stream has produced more than the
// caller's ceiling, the answer is DecompressedTooLarge whatever follows, and
// the cut that the decoder reports on the same read must not outrank it. A
// stream cut at or short of the ceiling is still corrupt, with the scheme's
// own code, since nothing crossed the ceiling there. Every cut is judged
// through DecompressBounded, over a dst that is absent, sized to the ceiling
// and sized to the plaintext, and through NewReader.
func Test_Ceiling_TruncatedPastMax(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		code errs.Code
		comp coretransform.Compressor
		//: a payload inside a decoder's window, and one past it, so a cut falls
		//: both before and after the decoder's first flush of plaintext.
		size int
	}
	gzipc := svctransform.NewGzipCompressor(flate.DefaultCompression)
	flatec := svctransform.NewFlateCompressor(flate.DefaultCompression)
	zlibc := svctransform.NewZlibCompressor(flate.DefaultCompression)
	tests := []tc{
		{"gzip small", coretransform.CodeGzipFailed, gzipc, 4096},
		{"gzip large", coretransform.CodeGzipFailed, gzipc, 40_000},
		{"flate small", coretransform.CodeFlateFailed, flatec, 4096},
		{"flate large", coretransform.CodeFlateFailed, flatec, 40_000},
		{"zlib small", coretransform.CodeZlibFailed, zlibc, 4096},
		{"zlib large", coretransform.CodeZlibFailed, zlibc, 40_000},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		packed, err := c.comp.Compress(nil, ceilingPayload(c.size))
		if err != nil {
			t.Fatalf("%s: Compress: %v", c.name, err)
		}
		bounded := c.comp.(coretransform.BoundedDecompressor)
		streaming := c.comp.(coretransform.StreamingCompressor)
		scheme := c.comp.Algorithm()
		crossed := 0
		for cutAt := 1; cutAt < len(packed); cutAt += max(7, len(packed)/100) {
			cut := packed[:cutAt]
			prefix, ok := ceilingPrefix(string(scheme), cut)
			//: no stream to read at all: the header's own refusal, not this test's.
			if !ok || len(prefix) < 2 {
				continue
			}
			held := int64(len(prefix))
			for _, ceiling := range []int64{0, 1, held - 1, held, held + 1} {
				past := held > ceiling
				if past {
					crossed++
				}
				for _, dst := range [][]byte{nil, make([]byte, 0, ceiling), make([]byte, 0, held+64)} {
					_, derr := bounded.DecompressBounded(dst, cut, ceiling)
					ceilingJudge(t, c.name+" DecompressBounded", c.code, cutAt, ceiling, past, derr)
				}
				rc, nerr := streaming.NewReader(bytes.NewReader(cut), ceiling)
				if nerr != nil {
					t.Fatalf("%s: NewReader: %v", c.name, nerr)
				}
				got, rerr := io.ReadAll(rc)
				//: never more than the ceiling reaches the caller.
				if int64(len(got)) > ceiling {
					t.Errorf("%s NewReader: cut %d ceiling %d: handed %d bytes", c.name, cutAt, ceiling, len(got))
				}
				ceilingJudge(t, c.name+" NewReader", c.code, cutAt, ceiling, past, rerr)
				if cerr := rc.Close(); cerr != nil {
					t.Errorf("%s NewReader: cut %d ceiling %d: Close = %v", c.name, cutAt, ceiling, cerr)
				}
			}
		}
		//: the sweep must have met cuts past the ceiling, or it proves nothing.
		if crossed == 0 {
			t.Fatalf("%s: no cut crossed a ceiling", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// ceilingJudge fails t unless err is the refusal the cut owes: the ceiling's
// when the decoder produced more than ceiling bytes, the scheme's fault when it
// did not.
func ceilingJudge(t *testing.T, label string, code errs.Code, cutAt int, ceiling int64, past bool, err error) {
	t.Helper()
	//: past the ceiling: too large, never the cut's fault.
	if past {
		if !errors.Is(err, coretransform.DecompressedTooLarge) {
			t.Errorf("%s: cut %d ceiling %d: err = %v, want DecompressedTooLarge", label, cutAt, ceiling, err)
		}
		return
	}
	//: within the ceiling: the stream is cut, the scheme's own code.
	if !errs.HasCode(err, code) {
		t.Errorf("%s: cut %d ceiling %d: err = %v, want code %v", label, cutAt, ceiling, err, code)
	}
}
