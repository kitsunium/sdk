// Package transform_test — the conformance table of the transform domain
// (kitsunium/sdk#1123): every contract a Compressor registered by this package
// must keep, written once and run over every engine the registry holds, so a
// rule added later reaches the three engines at once and an engine added later
// is held to every rule without an edit. What differs between engines — zlib's
// envelope, gzip's header — stays in the per-engine files.
package transform_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctransform "github.com/kitsunium/sdk/internal/service/data/transform"
)

// rowT is the part of *testing.T a conformance row reports through. A row
// takes it rather than *testing.T so TestABrokenEngineFailsTheRow can hand a
// row a recorder and watch it fail.
type rowT interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// conformanceRow is one contract, run against one engine.
type conformanceRow struct {
	name string
	run  func(t rowT, c coretransform.Compressor)
}

// engineCodes is the code each engine refuses a malformed stream with. It is
// the one per-engine fact the table needs, and TestEveryEngineHasItsCode fails
// for an engine registered without one, so a new engine cannot pass the table
// by being absent from it.
var engineCodes = map[coretransform.Algorithm]errs.Code{
	"gzip":  coretransform.CodeGzipFailed,
	"flate": coretransform.CodeFlateFailed,
	"zlib":  coretransform.CodeZlibFailed,
}

// everyCompressor returns every Compressor the registry holds, sorted by
// Algorithm: what importing this package registers, read from the registry
// and never listed by hand.
func everyCompressor(tb testing.TB) []coretransform.Compressor {
	tb.Helper()
	//: the import above is what fills the registry.
	_ = svctransform.GzipCompressor
	var out []coretransform.Compressor
	for _, algo := range coretransform.Available() {
		c, ok := coretransform.Lookup(algo)
		if !ok {
			tb.Fatalf("Available lists %q, Lookup misses it", algo)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		tb.Fatal("the registry is empty: the import did not register anything")
	}
	return out
}

// conformancePayloads are the plaintexts the rows round-trip: the shapes that
// break compressors — nothing, one byte, text, incompressible bytes.
func conformancePayloads() map[string][]byte {
	random := make([]byte, 64<<10)
	r := rand.New(rand.NewPCG(0x1123, 0x5eed))
	for i := range random {
		random[i] = byte(r.Uint32())
	}
	return map[string][]byte{
		"empty":  {},
		"byte":   {'k'},
		"text":   []byte(strings.Repeat(`{"level":"info","msg":"request served","status":200}`+"\n", 80)),
		"random": random,
	}
}

// conformanceRows is the table. Each row is a contract of the port, stated in
// its name.
func conformanceRows() []conformanceRow {
	return []conformanceRow{
		{"compress-then-decompress-is-the-identity-and-keeps-dst", rowRoundTrip},
		{"a-malformed-stream-is-the-engine's-refusal-and-dst-comes-back", rowGarbage},
		{"the-caller's-ceiling-trips-and-never-truncates", rowCeiling},
		{"a-stream-round-trips-and-writes-what-compress-writes", rowStreamRoundTrip},
		{"a-stream-hands-over-its-ceiling-then-refuses", rowStreamCeiling},
		{"a-malformed-stream-is-refused-through-the-reader", rowStreamGarbage},
		{"a-closed-stream-refuses-and-closes-once", rowStreamClosed},
	}
}

// TestConformance runs every row over every engine, the engine's Algorithm
// in the subtest's name.
func TestConformance(t *testing.T) {
	t.Parallel()
	for _, c := range everyCompressor(t) {
		for _, row := range conformanceRows() {
			t.Run(string(c.Algorithm())+"/"+row.name, func(t *testing.T) {
				t.Parallel()
				row.run(t, c)
			})
		}
	}
}

// TestTheImportRegistersTheThreeSchemes: importing the package registers
// gzip, flate and zlib, each under its canonical name — the registration the
// three per-engine tests used to pin one at a time.
func TestTheImportRegistersTheThreeSchemes(t *testing.T) {
	t.Parallel()
	for _, algo := range []coretransform.Algorithm{"gzip", "flate", "zlib"} {
		c, ok := coretransform.Lookup(algo)
		if !ok || c.Algorithm() != algo {
			t.Errorf("%q: registered=%v, want it under its own name", algo, ok)
		}
	}
}

// TestEveryEngineHasItsCode fails for a registered engine engineCodes does not
// name: the table would otherwise hold it to no refusal at all.
func TestEveryEngineHasItsCode(t *testing.T) {
	t.Parallel()
	for _, c := range everyCompressor(t) {
		if _, ok := engineCodes[c.Algorithm()]; !ok {
			t.Errorf("engine %q is registered with no refusal code in engineCodes", c.Algorithm())
		}
	}
}

// rowRoundTrip: Decompress(Compress(x)) is x, appended to a dst whose bytes
// are kept, for every payload shape; and a repetitive payload shrinks.
func rowRoundTrip(t rowT, c coretransform.Compressor) {
	t.Helper()
	for name, plain := range conformancePayloads() {
		encoded, err := c.Compress([]byte("head"), plain)
		if err != nil || !bytes.HasPrefix(encoded, []byte("head")) {
			t.Fatalf("%s: Compress err=%v, or the dst prefix was lost", name, err)
		}
		if name == "text" && len(encoded)-len("head") >= len(plain) {
			t.Fatalf("text: compressed %d >= input %d: an inert engine", len(encoded)-len("head"), len(plain))
		}
		back, derr := c.Decompress([]byte("prefix"), encoded[len("head"):])
		if derr != nil {
			t.Fatalf("%s: Decompress: %v", name, derr)
		}
		if !bytes.Equal(back, append([]byte("prefix"), plain...)) {
			t.Fatalf("%s: got %d bytes, want the prefix and the %d-byte payload", name, len(back), len(plain))
		}
	}
}

// rowGarbage: bytes that are no stream of the engine's are refused with the
// engine's code, and dst comes back as it was.
func rowGarbage(t rowT, c coretransform.Compressor) {
	t.Helper()
	want := engineCodes[c.Algorithm()]
	encoded, err := c.Compress(nil, []byte(strings.Repeat("payload ", 512)))
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	for name, garbage := range map[string][]byte{
		"not-a-stream": []byte("this is not a compressed stream at all, not even close"),
		"truncated":    encoded[:len(encoded)/2],
	} {
		dst := []byte("prefix")
		out, derr := c.Decompress(dst, garbage)
		if !errs.HasCode(derr, want) {
			t.Fatalf("%s: err=%v, want the engine's code %v", name, derr, want)
		}
		if !bytes.Equal(out, dst) {
			t.Fatalf("%s: a refusal returned %d bytes, want dst back", name, len(out))
		}
	}
}

// rowCeiling: DecompressBounded admits exactly max bytes and refuses one more
// with DecompressedTooLarge — never a truncated success — with dst back.
func rowCeiling(t rowT, c coretransform.Compressor) {
	t.Helper()
	bounded, ok := c.(coretransform.BoundedDecompressor)
	if !ok {
		t.Fatalf("engine %q is not a BoundedDecompressor", c.Algorithm())
	}
	plain := bytes.Repeat([]byte{'z'}, 4096)
	encoded, err := c.Compress(nil, plain)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if out, derr := bounded.DecompressBounded(nil, encoded, 4096); derr != nil || len(out) != 4096 {
		t.Fatalf("at the ceiling: err=%v, %d bytes, want the 4096", derr, len(out))
	}
	for _, ceiling := range []int64{4095, 1024, 0, -1} {
		dst := []byte("prefix")
		out, derr := bounded.DecompressBounded(dst, encoded, ceiling)
		if !errors.Is(derr, coretransform.DecompressedTooLarge) {
			t.Fatalf("ceiling %d: err=%v and %d bytes, want DecompressedTooLarge", ceiling, derr, len(out))
		}
		if !bytes.Equal(out, dst) {
			t.Fatalf("ceiling %d: a refusal returned %d bytes, want dst back", ceiling, len(out))
		}
	}
}

// streaming asserts c has a stream form.
func streaming(t rowT, c coretransform.Compressor) coretransform.StreamingCompressor {
	t.Helper()
	s, ok := c.(coretransform.StreamingCompressor)
	if !ok {
		t.Fatalf("engine %q is not a StreamingCompressor", c.Algorithm())
	}
	return s
}

// streamCompress writes plain through s's writer in uneven chunks and returns
// the stream.
func streamCompress(t rowT, s coretransform.StreamingCompressor, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := s.NewWriter(&out)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for rest := plain; len(rest) > 0; {
		n := min(len(rest), 1+len(rest)/3)
		if _, werr := w.Write(rest[:n]); werr != nil {
			t.Fatalf("Write: %v", werr)
		}
		rest = rest[n:]
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("writer Close: %v", cerr)
	}
	return out.Bytes()
}

// rowStreamRoundTrip: what NewWriter writes, NewReader reads back to the
// byte, ending in io.EOF; and for these engines it is what Compress writes.
func rowStreamRoundTrip(t rowT, c coretransform.Compressor) {
	t.Helper()
	s := streaming(t, c)
	for name, plain := range conformancePayloads() {
		stream := streamCompress(t, s, plain)
		block, err := c.Compress(nil, plain)
		if err != nil {
			t.Fatalf("%s: Compress: %v", name, err)
		}
		if !bytes.Equal(stream, block) {
			t.Fatalf("%s: the stream form is %d bytes, Compress wrote %d", name, len(stream), len(block))
		}
		r, rerr := s.NewReader(bytes.NewReader(stream), int64(len(plain)))
		if rerr != nil {
			t.Fatalf("%s: NewReader: %v", name, rerr)
		}
		back, aerr := io.ReadAll(r)
		if aerr != nil || !bytes.Equal(back, plain) {
			t.Fatalf("%s: read back err=%v, %d bytes, want %d", name, aerr, len(back), len(plain))
		}
		if cerr := r.Close(); cerr != nil {
			t.Fatalf("%s: reader Close: %v", name, cerr)
		}
	}
}

// readCounted drains r, reporting how many bytes it handed over and the error
// that stopped it.
func readCounted(r io.Reader) (n int64, err error) {
	n, err = io.Copy(io.Discard, r)
	return n, err
}

// rowStreamCeiling: a stream larger than the ceiling hands over exactly the
// ceiling's bytes, then fails with DecompressedTooLarge, and again on every
// read after; one that fits exactly ends in io.EOF; zero admits an empty
// stream only, and a negative ceiling is zero.
func rowStreamCeiling(t rowT, c coretransform.Compressor) {
	t.Helper()
	s := streaming(t, c)
	plain := bytes.Repeat([]byte{'s'}, 10*4096)
	stream := streamCompress(t, s, plain)
	for _, ceiling := range []int64{4096, int64(len(plain)) - 1, 0, -1} {
		r, err := s.NewReader(bytes.NewReader(stream), ceiling)
		if err != nil {
			t.Fatalf("ceiling %d: NewReader: %v", ceiling, err)
		}
		n, rerr := readCounted(r)
		if !errors.Is(rerr, coretransform.DecompressedTooLarge) || n != max(ceiling, 0) {
			t.Fatalf("ceiling %d: handed over %d bytes then err=%v, want %d then DecompressedTooLarge", ceiling, n, rerr, max(ceiling, 0))
		}
		if _, again := r.Read(make([]byte, 8)); !errors.Is(again, coretransform.DecompressedTooLarge) {
			t.Fatalf("ceiling %d: the refusal did not stick: %v", ceiling, again)
		}
		if cerr := r.Close(); cerr != nil {
			t.Fatalf("ceiling %d: Close: %v", ceiling, cerr)
		}
	}
	r, err := s.NewReader(bytes.NewReader(stream), int64(len(plain)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if n, rerr := readCounted(r); rerr != nil || n != int64(len(plain)) {
		t.Fatalf("exactly at the ceiling: %d bytes, err=%v, want %d and the end", n, rerr, len(plain))
	}
	empty := streamCompress(t, s, nil)
	for _, ceiling := range []int64{0, -1} {
		er, eerr := s.NewReader(bytes.NewReader(empty), ceiling)
		if eerr != nil {
			t.Fatalf("empty, ceiling %d: NewReader: %v", ceiling, eerr)
		}
		if n, rerr := readCounted(er); rerr != nil || n != 0 {
			t.Fatalf("an empty stream under ceiling %d: %d bytes, err=%v, want its end", ceiling, n, rerr)
		}
	}
}

// rowStreamGarbage: a stream that is not the engine's, or is cut short, fails
// with the engine's code — at NewReader where the format has a header to
// read, at the first read otherwise — and never ends in io.EOF.
func rowStreamGarbage(t rowT, c coretransform.Compressor) {
	t.Helper()
	s := streaming(t, c)
	want := engineCodes[c.Algorithm()]
	stream := streamCompress(t, s, []byte(strings.Repeat("payload ", 512)))
	for name, garbage := range map[string][]byte{
		"not-a-stream": []byte("this is not a compressed stream at all, not even close"),
		"truncated":    stream[:len(stream)/2],
	} {
		r, err := s.NewReader(bytes.NewReader(garbage), 1<<20)
		if err == nil {
			_, err = readCounted(r)
			if cerr := r.Close(); cerr != nil && !errs.HasCode(cerr, want) {
				t.Fatalf("%s: Close: %v", name, cerr)
			}
		}
		if !errs.HasCode(err, want) {
			t.Fatalf("%s: err=%v, want the engine's code %v", name, err, want)
		}
	}
}

// rowStreamClosed: after Close, a read and a write fail with the engine's
// code — the pooled decoder and encoder are someone else's by then — and a
// second Close is nil.
func rowStreamClosed(t rowT, c coretransform.Compressor) {
	t.Helper()
	s := streaming(t, c)
	want := engineCodes[c.Algorithm()]
	var sink bytes.Buffer
	w, err := s.NewWriter(&sink)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, werr := w.Write([]byte("before")); werr != nil {
		t.Fatalf("Write: %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	if _, werr := w.Write([]byte("after")); !errs.HasCode(werr, want) || !errors.Is(werr, io.ErrClosedPipe) {
		t.Fatalf("a write after Close: %v, want the engine's code over io.ErrClosedPipe", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("a second writer Close: %v", cerr)
	}
	r, rerr := s.NewReader(bytes.NewReader(sink.Bytes()), 1<<20)
	if rerr != nil {
		t.Fatalf("NewReader: %v", rerr)
	}
	if cerr := r.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	if _, rerr = r.Read(make([]byte, 8)); !errs.HasCode(rerr, want) {
		t.Fatalf("a read after Close: %v, want the engine's code", rerr)
	}
	if cerr := r.Close(); cerr != nil {
		t.Fatalf("a second reader Close: %v", cerr)
	}
}

// rowRecorder is the rowT a broken engine's row reports to: it records a
// failure, and stops the row on Fatalf as *testing.T does.
type rowRecorder struct {
	failures []string
}

// rowStop is what rowRecorder.Fatalf panics with, recovered by runRecorded.
type rowStop struct{}

// Helper implements rowT.
func (*rowRecorder) Helper() {}

// Errorf implements rowT: the failure is recorded.
func (r *rowRecorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// Fatalf implements rowT: the failure is recorded and the row stops.
func (r *rowRecorder) Fatalf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
	panic(rowStop{})
}

// runRecorded runs row against c with a recorder and returns what it recorded.
func runRecorded(row conformanceRow, c coretransform.Compressor) (failures []string) {
	rec := &rowRecorder{}
	defer func() {
		//: a Fatalf's stop is expected; any other panic is the row's own.
		if p := recover(); p != nil {
			if _, ok := p.(rowStop); !ok {
				panic(p)
			}
		}
		failures = rec.failures
	}()
	row.run(rec, c)
	return rec.failures
}

// capIgnoringEngine is a deliberately broken engine: gzip, except that its
// DecompressBounded ignores the caller's ceiling and its stream reader hands
// over the whole stream.
type capIgnoringEngine struct{ coretransform.Compressor }

// Algorithm names the double after the engine it breaks, so its code is
// gzip's.
func (e capIgnoringEngine) Algorithm() coretransform.Algorithm { return "gzip" }

// DecompressBounded ignores max — the defect the row exists to catch.
func (e capIgnoringEngine) DecompressBounded(dst, src []byte, _ int64) ([]byte, error) {
	return e.Decompress(dst, src)
}

// NewReader ignores max as well.
func (e capIgnoringEngine) NewReader(r io.Reader, _ int64) (io.ReadCloser, error) {
	return e.Compressor.(coretransform.StreamingCompressor).NewReader(r, 1<<40)
}

// NewWriter is gzip's.
func (e capIgnoringEngine) NewWriter(w io.Writer) (io.WriteCloser, error) {
	return e.Compressor.(coretransform.StreamingCompressor).NewWriter(w)
}

// TestABrokenEngineFailsTheRow is #1123's third acceptance line: an engine
// whose decompression ignores the ceiling fails the two ceiling rows, while
// passing the rows it does not break — so the table catches the defect, and
// catches only it.
func TestABrokenEngineFailsTheRow(t *testing.T) {
	t.Parallel()
	broken := capIgnoringEngine{svctransform.GzipCompressor}
	for _, row := range conformanceRows() {
		failures := runRecorded(row, broken)
		breaks := strings.Contains(row.name, "ceiling")
		if breaks && len(failures) == 0 {
			t.Errorf("%s: the broken engine passed", row.name)
		}
		if !breaks && len(failures) > 0 {
			t.Errorf("%s: an engine broken elsewhere failed it: %v", row.name, failures)
		}
	}
}
