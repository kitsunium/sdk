package transform_test

import (
	"bytes"
	"errors"
	"fmt"
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

// TestAStreamWritesToWhatItReads: io.Copy drains a stream through its
// WriteTo (#892), and hands the caller's writer what Read hands over, under
// every ceiling — all of it and the end, or exactly the ceiling's bytes and
// DecompressedTooLarge, sticky through both WriteTo and Read — after a
// first Read or none.
func TestAStreamWritesToWhatItReads(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		sc      streamScheme
		ceiling int64
		first   int
	}
	plain := bytes.Repeat([]byte("copied, never buffered "), 40000)
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		stream := writeStream(t, c.sc.c, plain)
		r, err := c.sc.c.NewReader(bytes.NewReader(stream), c.ceiling)
		if err != nil {
			t.Fatalf("%s: NewReader: %v", c.name, err)
		}
		defer func() {
			if cerr := r.Close(); cerr != nil {
				t.Errorf("%s: Close: %v", c.name, cerr)
			}
		}()
		if _, ok := r.(io.WriterTo); !ok {
			t.Fatalf("%s: the stream is not an io.WriterTo", c.name)
		}
		head := make([]byte, c.first)
		if _, rerr := io.ReadFull(r, head); rerr != nil {
			t.Fatalf("%s: the first Read: %v", c.name, rerr)
		}
		var out bytes.Buffer
		_, cerr := io.Copy(&out, r)
		got := append(head, out.Bytes()...)
		fits := c.ceiling >= int64(len(plain))
		handed := min(max(c.ceiling, 0), int64(len(plain)))
		if !bytes.Equal(got, plain[:handed]) {
			t.Fatalf("%s: handed over %d bytes, want the first %d", c.name, len(got), handed)
		}
		if fits && cerr != nil || !fits && !errors.Is(cerr, coretransform.DecompressedTooLarge) {
			t.Fatalf("%s: io.Copy err=%v, fits=%v", c.name, cerr, fits)
		}
		_, again := r.(io.WriterTo).WriteTo(io.Discard)
		_, read := r.Read(make([]byte, 8))
		if fits && (again != nil || read != io.EOF) || !fits && (!errors.Is(again, coretransform.DecompressedTooLarge) || !errors.Is(read, coretransform.DecompressedTooLarge)) {
			t.Fatalf("%s: after the outcome WriteTo=%v, Read=%v", c.name, again, read)
		}
	}
	for _, sc := range streamSchemes(t) {
		for _, ceiling := range []int64{int64(len(plain)), int64(len(plain)) + 1, int64(len(plain)) - 1, 300 << 10, 1, 0, -1} {
			for _, first := range []int{0, 1} {
				//: a first Read needs a byte the ceiling admits.
				if first > 0 && ceiling < 1 {
					continue
				}
				runCase(t, tc{fmt.Sprintf("%s/ceiling %d/first %d", sc.name, ceiling, first), sc, ceiling, first})
			}
		}
	}
}

// TestAStreamStopsWhereItsWriterDoes: a caller's writer that fails stops
// io.Copy with that very error, unwrapped as io.Copy returns it, and the
// stream is spent: every read after returns it too. One that takes less
// than it is given without an error stops it with io.ErrShortWrite.
func TestAStreamStopsWhereItsWriterDoes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		sc   streamScheme
		w    *refusingWriter
		want error
	}
	stream := func(sc streamScheme) []byte {
		return writeStream(t, sc.c, bytes.Repeat([]byte("refused "), 100000))
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r, err := c.sc.c.NewReader(bytes.NewReader(stream(c.sc)), 1<<30)
		if err != nil {
			t.Fatalf("%s: NewReader: %v", c.name, err)
		}
		n, cerr := io.Copy(c.w, r)
		if cerr != c.want || n != c.w.took {
			t.Fatalf("%s: io.Copy wrote %d (%d taken), err=%v, want %v", c.name, n, c.w.took, cerr, c.want)
		}
		if _, again := r.Read(make([]byte, 8)); again != c.want {
			t.Fatalf("%s: a Read after it: %v, want %v", c.name, again, c.want)
		}
		if closeErr := r.Close(); closeErr != nil {
			t.Fatalf("%s: Close: %v", c.name, closeErr)
		}
	}
	for _, sc := range streamSchemes(t) {
		runCase(t, tc{sc.name + "/failing", sc, &refusingWriter{take: 1 << 10, fail: io.ErrClosedPipe}, io.ErrClosedPipe})
		runCase(t, tc{sc.name + "/short", sc, &refusingWriter{take: 1 << 10}, io.ErrShortWrite})
	}
}

// refusingWriter takes take bytes of the first write it is handed, then
// returns fail — nil for a writer that takes less without saying why.
type refusingWriter struct {
	take int
	took int64
	fail error
}

// Write implements io.Writer.
func (w *refusingWriter) Write(p []byte) (n int, err error) {
	n = min(w.take, len(p))
	w.took += int64(n)
	return n, w.fail
}

// TestAWriterFaultIsNeverTheEnd: a destination that returns io.EOF — the one
// value a reader takes for a stream's end — failed to take the bytes: WriteTo
// returns it as it came, with the count the destination accepted, and neither
// a second WriteTo nor a Read takes the stream for a complete one (#892).
// zstd's Decoder.WriteTo keeps such an error and returns nil, so the stream
// must tell the fault by the failed write, not by the decoder's answer.
func TestAWriterFaultIsNeverTheEnd(t *testing.T) {
	t.Parallel()
	plain := bytes.Repeat([]byte("the destination stops listening "), 40000)
	runCase := func(t *testing.T, sc streamScheme) {
		t.Helper()
		r, err := sc.c.NewReader(bytes.NewReader(writeStream(t, sc.c, plain)), int64(len(plain)))
		if err != nil {
			t.Fatalf("%s: NewReader: %v", sc.name, err)
		}
		defer func() {
			if cerr := r.Close(); cerr != nil {
				t.Errorf("%s: Close: %v", sc.name, cerr)
			}
		}()
		w := &endingWriter{fail: io.EOF}
		n, werr := r.(io.WriterTo).WriteTo(w)
		if werr != io.EOF || n != w.took || n == 0 || n >= int64(len(plain)) {
			t.Fatalf("%s: WriteTo wrote %d (%d accepted of %d), err=%v, want io.EOF", sc.name, n, w.took, len(plain), werr)
		}
		again, aerr := r.(io.WriterTo).WriteTo(io.Discard)
		if aerr == nil || aerr == io.EOF || again != 0 {
			t.Fatalf("%s: a second WriteTo wrote %d, err=%v: the stream is cut, not complete", sc.name, again, aerr)
		}
		_, rerr := r.Read(make([]byte, 8))
		if rerr == nil || rerr == io.EOF {
			t.Fatalf("%s: a Read after it: %v: the stream is cut, not complete", sc.name, rerr)
		}
		if !errs.HasCode(aerr, sc.code) || !errs.HasCode(rerr, sc.code) {
			t.Fatalf("%s: after it WriteTo=%v, Read=%v, want the scheme's code", sc.name, aerr, rerr)
		}
	}
	for _, sc := range streamSchemes(t) {
		t.Run(sc.name, func(t *testing.T) { runCase(t, sc) })
	}
}

// TestAWriterFaultOfAnUncomparableTypeIsReturnedAsIs: the identity of a
// destination's fault is the fact that its last write failed, never a
// comparison of values, which cannot be made of every type: an error holding a
// slice reaches the caller as it was, not wrapped in the scheme's sentinel.
func TestAWriterFaultOfAnUncomparableTypeIsReturnedAsIs(t *testing.T) {
	t.Parallel()
	plain := bytes.Repeat([]byte("an error that cannot be compared "), 40000)
	runCase := func(t *testing.T, sc streamScheme) {
		t.Helper()
		r, err := sc.c.NewReader(bytes.NewReader(writeStream(t, sc.c, plain)), int64(len(plain)))
		if err != nil {
			t.Fatalf("%s: NewReader: %v", sc.name, err)
		}
		defer func() {
			if cerr := r.Close(); cerr != nil {
				t.Errorf("%s: Close: %v", sc.name, cerr)
			}
		}()
		w := &endingWriter{fail: uncomparableFault{reasons: []string{"no space", "left"}}}
		n, werr := r.(io.WriterTo).WriteTo(w)
		var got uncomparableFault
		if !errors.As(werr, &got) || len(got.reasons) != 2 || errs.HasCode(werr, sc.code) || n != w.took {
			t.Fatalf("%s: WriteTo wrote %d (%d accepted), err=%v, want the destination's own error", sc.name, n, w.took, werr)
		}
		_, aerr := r.(io.WriterTo).WriteTo(io.Discard)
		if !errors.As(aerr, &got) {
			t.Fatalf("%s: a second WriteTo: %v, want the same fault kept", sc.name, aerr)
		}
	}
	for _, sc := range streamSchemes(t) {
		t.Run(sc.name, func(t *testing.T) { runCase(t, sc) })
	}
}

// uncomparableFault is an error whose type cannot be compared with ==.
type uncomparableFault struct{ reasons []string }

// Error implements error.
func (f uncomparableFault) Error() string { return "uncomparable fault" }

// endingWriter takes its first write whole, then half of the second and
// returns fail with it.
type endingWriter struct {
	writes int
	took   int64
	fail   error
}

// Write implements io.Writer.
func (w *endingWriter) Write(p []byte) (n int, err error) {
	w.writes++
	//: the first write is taken whole.
	if w.writes == 1 {
		w.took += int64(len(p))
		return len(p), nil
	}
	n = len(p) / 2
	w.took += int64(n)
	return n, w.fail
}
