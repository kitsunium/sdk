package transform_test

import (
	"bytes"
	"io"
	"runtime"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	tptransform "github.com/kitsunium/sdk/third-party/transform"
)

// streamScheme is one scheme's stream form, with the code its malformed
// streams are refused with.
type streamScheme struct {
	name string
	c    coretransform.StreamingCompressor
	code errs.Code
}

// streamSchemes returns zstd and s2 as the registry holds them: the
// singletons are StreamingCompressors, so the facade reaches their streams
// through a type assertion.
func streamSchemes(tb testing.TB) []streamScheme {
	tb.Helper()
	var out []streamScheme
	for _, c := range []struct {
		algo coretransform.Algorithm
		code errs.Code
	}{{tptransform.ZstdAlgorithm, tptransform.CodeZstdFailed}, {tptransform.S2Algorithm, tptransform.CodeS2Failed}} {
		registered, ok := coretransform.Lookup(c.algo)
		if !ok {
			tb.Fatalf("%s is not registered", c.algo)
		}
		s, ok := registered.(coretransform.StreamingCompressor)
		if !ok {
			tb.Fatalf("the registered %s is not a StreamingCompressor", c.algo)
		}
		out = append(out, streamScheme{string(c.algo), s, c.code})
	}
	return out
}

// writeStream compresses plain through c's writer in uneven chunks.
func writeStream(tb testing.TB, c coretransform.StreamingCompressor, plain []byte) []byte {
	tb.Helper()
	var out bytes.Buffer
	w, err := c.NewWriter(&out)
	if err != nil {
		tb.Fatalf("NewWriter: %v", err)
	}
	for rest := plain; len(rest) > 0; {
		n := min(len(rest), 1+len(rest)/3)
		if _, werr := w.Write(rest[:n]); werr != nil {
			tb.Fatalf("Write: %v", werr)
		}
		rest = rest[n:]
	}
	if cerr := w.Close(); cerr != nil {
		tb.Fatalf("Close: %v", cerr)
	}
	return out.Bytes()
}

// readStream drains c's reader over stream under limit.
func readStream(tb testing.TB, c coretransform.StreamingCompressor, stream []byte, limit int64) ([]byte, error) {
	tb.Helper()
	r, err := c.NewReader(bytes.NewReader(stream), limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := r.Close(); cerr != nil {
			tb.Errorf("reader Close: %v", cerr)
		}
	}()
	return io.ReadAll(r)
}

// TestAStreamRoundTrips writes and reads back every payload shape through
// both schemes' streams, and checks that neither spawned a goroutine it left
// behind: both run their codec in the caller's goroutine.
func TestAStreamRoundTrips(t *testing.T) {
	before := runtime.NumGoroutine()
	for _, sc := range streamSchemes(t) {
		for _, p := range payloads(t) {
			stream := writeStream(t, sc.c, p.bytes)
			back, err := readStream(t, sc.c, stream, int64(len(p.bytes)))
			if err != nil || !bytes.Equal(back, p.bytes) {
				t.Fatalf("%s/%s: err=%v, %d bytes, want %d", sc.name, p.name, err, len(back), len(p.bytes))
			}
		}
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("%d goroutines before the streams, %d after", before, after)
	}
}

// TestTheStreamFormsAgainstTheBlockForms pins the one wire difference
// between the two schemes: a zstd stream is frames, so Decompress reads what
// NewWriter writes and NewReader what Compress writes; an s2 stream is the
// framing format, which Decompress refuses as a malformed block, and an s2
// block is refused by NewReader — S2Failed both ways, never a misread.
func TestTheStreamFormsAgainstTheBlockForms(t *testing.T) {
	t.Parallel()
	plain := bytes.Repeat([]byte("stream or block "), 512)
	zs := streamSchemes(t)[0]
	stream := writeStream(t, zs.c, plain)
	if back, err := tptransform.Zstd.Decompress(nil, stream); err != nil || !bytes.Equal(back, plain) {
		t.Fatalf("zstd: Decompress of a stream: err=%v, %d bytes", err, len(back))
	}
	block, err := tptransform.Zstd.Compress(nil, plain)
	if err != nil {
		t.Fatalf("zstd: Compress: %v", err)
	}
	if back, rerr := readStream(t, zs.c, block, int64(len(plain))); rerr != nil || !bytes.Equal(back, plain) {
		t.Fatalf("zstd: NewReader of a block: err=%v, %d bytes", rerr, len(back))
	}

	s2s := streamSchemes(t)[1]
	stream = writeStream(t, s2s.c, plain)
	if _, derr := tptransform.S2.Decompress(nil, stream); !errs.HasCode(derr, tptransform.CodeS2Failed) {
		t.Fatalf("s2: Decompress of a stream: err=%v, want S2Failed", derr)
	}
	block, err = tptransform.S2.Compress(nil, plain)
	if err != nil {
		t.Fatalf("s2: Compress: %v", err)
	}
	if _, rerr := readStream(t, s2s.c, block, int64(len(plain))); !errs.HasCode(rerr, tptransform.CodeS2Failed) {
		t.Fatalf("s2: NewReader of a block: err=%v, want S2Failed", rerr)
	}
}

// TestAStreamWindowPastTheCeilingIsABomb: a zstd stream reader's memory is
// its window, bounded by the compressor's ceiling — so a stream declaring a
// window past it is refused at its header with DecompressionLimitExceeded,
// the bomb code, whatever limit the caller passed.
func TestAStreamWindowPastTheCeilingIsABomb(t *testing.T) {
	t.Parallel()
	small, err := tptransform.NewZstdCompressor(tptransform.ZstdFastest, 64<<10)
	if err != nil {
		t.Fatalf("NewZstdCompressor: %v", err)
	}
	t.Cleanup(func() { closeChecked(t, small) })
	stream := writeStream(t, streamSchemes(t)[0].c, bytes.Repeat([]byte("a window of megabytes "), 1<<16))
	_, rerr := readStream(t, small, stream, 1<<30)
	if !errs.HasCode(rerr, tptransform.CodeDecompressionLimitExceeded) {
		t.Fatalf("err=%v, want DecompressionLimitExceeded", rerr)
	}
}

// TestADictionaryStreamNeedsItsDictionary: a stream written with a dictionary
// reads back through the compressor holding it, and is DictionaryMismatch
// through one without.
func TestADictionaryStreamNeedsItsDictionary(t *testing.T) {
	t.Parallel()
	records := dictionaryRecords(t, 200)
	withA := newDictionaryCompressor(t, trainDictionary(t, dictionaryIDA, records))
	plain := bytes.Join(records[150:], nil)
	stream := writeStream(t, withA, plain)
	if back, err := readStream(t, withA, stream, int64(len(plain))); err != nil || !bytes.Equal(back, plain) {
		t.Fatalf("with its dictionary: err=%v, %d bytes, want %d", err, len(back), len(plain))
	}
	_, rerr := readStream(t, streamSchemes(t)[0].c, stream, int64(len(plain)))
	if !errs.HasCode(rerr, tptransform.CodeDictionaryMismatch) {
		t.Fatalf("without it: err=%v, want DictionaryMismatch", rerr)
	}
}
