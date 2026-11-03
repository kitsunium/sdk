// Package transform_test — the vendor module's half of the transform
// domain's conformance table (kitsunium/sdk#1123): the contracts of
// internal/service/data/transform's conformance_external_test.go, run over
// every engine the registry holds in this test binary, read from it so an
// engine added here is held to every row without an edit. That is zstd and s2,
// and gzip, flate and zlib too: transform_bench_test.go imports the stdlib
// package to compare against it, which registers them.
//
// The table lives twice because it cannot live once: internal/service's test
// cannot import this module — the module requires internal, and no layer
// below third-party may reach it (scripts/check-layer-deps.sh) — and a _test
// file is importable by nobody. An importable suite is #985's. Two rows of the
// stdlib table do not transfer and are left out on purpose: the caller's
// ceiling on the block path (DecompressBounded), which zstd and s2 do not
// implement until #917 — their block ceiling is the instance's, held by
// bomb_external_test.go — and "the stream form is what Compress writes",
// which s2's framing format is not (TestTheStreamFormsAgainstTheBlockForms).
package transform_test

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"strings"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	tptransform "github.com/kitsunium/sdk/third-party/transform"
)

// vendorRow is one contract, run against one engine.
type vendorRow struct {
	name string
	run  func(t *testing.T, c coretransform.Compressor)
}

// registeredCodes is the code each engine refuses a malformed stream with —
// the one per-engine fact the table needs. TestEveryRegisteredEngineHasItsCode
// fails for an engine registered without one.
var registeredCodes = map[coretransform.Algorithm]errs.Code{
	tptransform.ZstdAlgorithm: tptransform.CodeZstdFailed,
	tptransform.S2Algorithm:   tptransform.CodeS2Failed,
	"gzip":                    coretransform.CodeGzipFailed,
	"flate":                   coretransform.CodeFlateFailed,
	"zlib":                    coretransform.CodeZlibFailed,
}

// everyRegisteredCompressor returns every Compressor the registry holds in
// this test binary, sorted by Algorithm: what the imports register, read from
// the registry and never listed by hand.
func everyRegisteredCompressor(tb testing.TB) []coretransform.Compressor {
	tb.Helper()
	//: the import above is what fills the registry.
	_ = tptransform.Zstd
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

// vendorPayloads are the plaintexts the rows round-trip: nothing, one byte,
// text, incompressible bytes — the stdlib table's shapes.
func vendorPayloads() map[string][]byte {
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

// vendorRows is the table; each row's name states its contract, worded as the
// stdlib table words it.
func vendorRows() []vendorRow {
	return []vendorRow{
		{"compress-then-decompress-is-the-identity-and-keeps-dst", vendorRowRoundTrip},
		{"a-malformed-stream-is-the-engine's-refusal-and-dst-comes-back", vendorRowGarbage},
		{"a-stream-round-trips", vendorRowStreamRoundTrip},
		{"a-stream-hands-over-its-ceiling-then-refuses", vendorRowStreamCeiling},
		{"a-malformed-stream-is-refused-through-the-reader", vendorRowStreamGarbage},
		{"a-closed-stream-refuses-and-closes-once", vendorRowStreamClosed},
	}
}

// TestVendorConformance runs every row over every engine, the engine's
// Algorithm in the subtest's name.
func TestVendorConformance(t *testing.T) {
	t.Parallel()
	for _, c := range everyRegisteredCompressor(t) {
		for _, row := range vendorRows() {
			t.Run(string(c.Algorithm())+"/"+row.name, func(t *testing.T) {
				t.Parallel()
				row.run(t, c)
			})
		}
	}
}

// TestEveryRegisteredEngineHasItsCode fails for a registered engine
// registeredCodes does not name: the table would otherwise hold it to no
// refusal at all.
func TestEveryRegisteredEngineHasItsCode(t *testing.T) {
	t.Parallel()
	for _, c := range everyRegisteredCompressor(t) {
		if _, ok := registeredCodes[c.Algorithm()]; !ok {
			t.Errorf("engine %q is registered with no refusal code in registeredCodes", c.Algorithm())
		}
	}
}

// vendorRowRoundTrip: Decompress(Compress(x)) is x, appended to a dst whose
// bytes are kept, for every payload shape; and a repetitive payload shrinks.
func vendorRowRoundTrip(t *testing.T, c coretransform.Compressor) {
	t.Helper()
	for name, plain := range vendorPayloads() {
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

// vendorRowGarbage: bytes that are no stream of the engine's are refused with
// the engine's code, and dst comes back as it was.
func vendorRowGarbage(t *testing.T, c coretransform.Compressor) {
	t.Helper()
	want := registeredCodes[c.Algorithm()]
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

// vendorStreaming asserts c has a stream form.
func vendorStreaming(t *testing.T, c coretransform.Compressor) coretransform.StreamingCompressor {
	t.Helper()
	s, ok := c.(coretransform.StreamingCompressor)
	if !ok {
		t.Fatalf("engine %q is not a StreamingCompressor", c.Algorithm())
	}
	return s
}

// vendorRowStreamRoundTrip: what NewWriter writes, NewReader reads back to the
// byte, ending in io.EOF, for every payload shape.
func vendorRowStreamRoundTrip(t *testing.T, c coretransform.Compressor) {
	t.Helper()
	s := vendorStreaming(t, c)
	for name, plain := range vendorPayloads() {
		back, err := readStream(t, s, writeStream(t, s, plain), int64(len(plain)))
		if err != nil || !bytes.Equal(back, plain) {
			t.Fatalf("%s: read back err=%v, %d bytes, want %d", name, err, len(back), len(plain))
		}
	}
}

// vendorReadCounted drains r, reporting how many bytes it handed over and the
// error that stopped it.
func vendorReadCounted(r io.Reader) (n int64, err error) {
	n, err = io.Copy(io.Discard, r)
	return n, err
}

// vendorRowStreamCeiling: a stream larger than the ceiling hands over exactly
// the ceiling's bytes, then fails with DecompressedTooLarge, and again on
// every read after; one that fits exactly ends in io.EOF; zero admits an
// empty stream only, and a negative ceiling is zero.
func vendorRowStreamCeiling(t *testing.T, c coretransform.Compressor) {
	t.Helper()
	s := vendorStreaming(t, c)
	plain := bytes.Repeat([]byte{'s'}, 10*4096)
	stream := writeStream(t, s, plain)
	for _, ceiling := range []int64{4096, int64(len(plain)) - 1, 0, -1} {
		r, err := s.NewReader(bytes.NewReader(stream), ceiling)
		if err != nil {
			t.Fatalf("ceiling %d: NewReader: %v", ceiling, err)
		}
		n, rerr := vendorReadCounted(r)
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
	if back, err := readStream(t, s, stream, int64(len(plain))); err != nil || len(back) != len(plain) {
		t.Fatalf("exactly at the ceiling: %d bytes, err=%v, want %d and the end", len(back), err, len(plain))
	}
	empty := writeStream(t, s, nil)
	for _, ceiling := range []int64{0, -1} {
		if back, err := readStream(t, s, empty, ceiling); err != nil || len(back) != 0 {
			t.Fatalf("an empty stream under ceiling %d: %d bytes, err=%v, want its end", ceiling, len(back), err)
		}
	}
}

// vendorRowStreamGarbage: a stream that is not the engine's, or is cut short,
// fails with the engine's code — at NewReader or at the first read — and
// never ends in io.EOF.
func vendorRowStreamGarbage(t *testing.T, c coretransform.Compressor) {
	t.Helper()
	s := vendorStreaming(t, c)
	want := registeredCodes[c.Algorithm()]
	stream := writeStream(t, s, []byte(strings.Repeat("payload ", 512)))
	for name, garbage := range map[string][]byte{
		"not-a-stream": []byte("this is not a compressed stream at all, not even close"),
		"truncated":    stream[:len(stream)/2],
	} {
		r, err := s.NewReader(bytes.NewReader(garbage), 1<<20)
		if err == nil {
			_, err = vendorReadCounted(r)
			if cerr := r.Close(); cerr != nil && !errs.HasCode(cerr, want) {
				t.Fatalf("%s: Close: %v", name, cerr)
			}
		}
		if !errs.HasCode(err, want) {
			t.Fatalf("%s: err=%v, want the engine's code %v", name, err, want)
		}
	}
}

// vendorRowStreamClosed: after Close, a read and a write fail with the
// engine's code over io.ErrClosedPipe — the pooled decoder and encoder are
// someone else's by then — and a second Close is nil.
func vendorRowStreamClosed(t *testing.T, c coretransform.Compressor) {
	t.Helper()
	s := vendorStreaming(t, c)
	want := registeredCodes[c.Algorithm()]
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
	if _, rerr = r.Read(make([]byte, 8)); !errs.HasCode(rerr, want) || !errors.Is(rerr, io.ErrClosedPipe) {
		t.Fatalf("a read after Close: %v, want the engine's code over io.ErrClosedPipe", rerr)
	}
	if cerr := r.Close(); cerr != nil {
		t.Fatalf("a second reader Close: %v", cerr)
	}
}
