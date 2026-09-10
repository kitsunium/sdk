// Package transform — white-box tests for the recycled gzip, flate and zlib
// codecs in pool.go: the reuse path, the guard that keeps the three pools
// apart, and the invariant that lets releaseZlibWriter carry no nil-pool
// branch.
//
// They are internal because the entire subject is unexported — the pools, the
// readerBox and its scheme tag, and the take/release pairs. And nothing here
// calls t.Parallel, which is the point rather than caution: every test drives a
// package-level sync.Pool, and a parallel sibling doing the same would turn a
// hit or a miss into a coin toss. Go runs the serial top-level tests to
// completion before releasing the parallel ones, so these run with the pools to
// themselves.
//
// Forcing a pool HIT is the difficulty these tests exist to answer, and it is
// why a regression on the reuse path could ship green before them. sync.Pool
// offers no API for it: Put lands in the current P's private slot, Get reads it
// back, and a GC or a P migration in between silently turns the pair into a
// miss. So nothing here assumes a hit — primedBox leaves the pool holding one
// known box, and hitDecode ATTRIBUTES the take that follows before using its
// verdict, retrying while the attribution fails rather than settling for a
// quietly weaker assertion. The MISS reference is never a hoped-for miss
// either: it is the stdlib constructor the cold path itself calls, used
// directly with no pool involved at all.
package transform

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// poolHitAttempts bounds hitDecode's retry loop. One attempt suffices on
	// almost every run; the loop exists so that a take this goroutine's P
	// migration turned into a miss costs a retry instead of weakening the
	// assertion that follows — which is a failure seen under -race.
	poolHitAttempts int = 64
	// poolTestPayload repeats enough to fill an encoder's history window and to
	// give a decoder's dictionary something to carry, so a writer or reader
	// recycled WITHOUT a Reset shows it in the bytes rather than by luck.
	poolTestPayload = "the quick brown fox jumps over the lazy dog, " +
		"and then the quick brown fox does it again, and again, and again"
	// poolLevelSweepLow is the lowest level the band test probes: far enough
	// under zlib.HuffmanOnly (-2) to cover the refused side.
	poolLevelSweepLow int = -8
	// poolLevelSweepHigh is the highest level the band test probes: far enough
	// over zlib.BestCompression (9) to cover the refused side.
	poolLevelSweepHigh int = 16
	// poolHealRounds is how many decodes follow a corrupted pool. More than one,
	// because healing that only lasts for the take that found the foreign
	// decoder is not healing.
	poolHealRounds int = 4
	// poolDrainAttempts bounds emptyPool's loop. A pool holds at most one box
	// per P plus a victim generation, so this is orders of magnitude over.
	poolDrainAttempts int = 1024
	// poolWedgeRounds is how many refusal-then-decode pairs the wedge test
	// alternates. A pool wedged by a refusal fails on the round after it, so a
	// single pair would only ever prove the first refusal survivable.
	poolWedgeRounds int = 8
)

// readerSchemeCase describes one scheme's decoder pool completely enough to
// drive it from outside: the take/release pair, the cold-path constructor used
// as the miss reference, a frame the scheme accepts, one it must refuse, and
// the sentinel a refusal carries at the package edge.
type readerSchemeCase struct {
	// name identifies the scheme in failure messages.
	name string
	// tag is the readerScheme value this pool's boxes must carry.
	tag readerScheme
	// take is the scheme's pooled decoder borrow.
	take func(src io.Reader) (*readerBox, error)
	// release is the matching return; it is also how a corrupted pool is built.
	release func(box *readerBox)
	// rawGet is the pool's own Get, unmediated by a take. It is what lets a
	// test EMPTY the pool, which is what makes a subsequent Put-then-take
	// deterministic instead of a race against whatever the private slot held.
	rawGet func() *readerBox
	// fresh is the constructor the take's own miss branch calls, used directly
	// so the miss reference is the real thing and never a hoped-for miss.
	fresh func(src io.Reader) (io.ReadCloser, error)
	// good is a frame this scheme decodes to poolTestPayload.
	good []byte
	// bad is a frame this scheme must refuse, on either path.
	bad []byte
	// decompress is the package-edge entry point, for the sentinel assertions.
	decompress func(dst, src []byte) (decoded []byte, err error)
	// code is the dotted-quad code a refusal must carry at that edge.
	code errs.Code
}

// writerSchemeCase describes one scheme's encoder pool: one pooled encode cycle
// whose encoder identity is observable, and the stdlib-only encode the take's
// cold path performs, which is the miss reference.
type writerSchemeCase struct {
	// name identifies the scheme (and, for zlib, the level) in messages.
	name string
	// cycle runs one take-write-close-release through the pool and returns the
	// wire bytes plus the pooled encoder's identity, so reuse is provable.
	cycle func(tb testing.TB, src []byte) (wire []byte, identity any)
	// fresh encodes through a stdlib writer built exactly as the cold path
	// builds it, with no pool involved.
	fresh func(tb testing.TB, src []byte) (wire []byte)
	// rawGet takes one encoder straight out of the pool and reports whether the
	// pool was empty (every writer factory yields nil on a miss). It is what
	// lets a test EMPTY the pool before proving a reuse.
	rawGet func() (empty bool)
}

// drainReader reads rc to EOF and folds a Close fault into the read error,
// which is exactly what gzipDecompress, flateDecompress and zlibDecompress each
// do around the pool. It is mirrored here rather than borrowed from one of them
// so a probe can compare the hit and miss paths AT THE TAKE, where they
// actually differ, instead of only at the package edge.
func drainReader(rc io.ReadCloser) (plain []byte, err error) {
	//: the fixtures are a few hundred bytes, so overflow is not reachable here
	//: and the production ceiling keeps the helper honest to the real call.
	plain, _, err = readAllBounded(rc, maxDecompressedBytes)
	//: a Close fault after a clean drain still fails the decode.
	if cerr := rc.Close(); cerr != nil && err == nil {
		//: surface the Close fault as this decode's error.
		err = cerr
	}
	//: hand back whatever the drain produced.
	return plain, err
}

// drainFresh decodes wire through the scheme's own cold-path constructor with
// no pool involved. This is the miss-path reference: takeGzipReader's miss
// branch IS gzip.NewReader, takeZlibReader's IS zlib.NewReader, and
// takeFlateReader's IS flate.NewReader — so comparing against it compares the
// reuse path against the construction path and against nothing else.
func drainFresh(sc readerSchemeCase, wire []byte) (plain []byte, err error) {
	rc, cerr := sc.fresh(bytes.NewReader(wire))
	//: a constructor that refused the stream IS the miss path's whole verdict.
	if cerr != nil {
		//: no decoder to drain — hand the refusal back as the result.
		return nil, cerr
	}
	//: drain and fold Close exactly as the schemes do around the pool.
	return drainReader(rc)
}

// emptyPool takes boxes out of sc's pool until one comes back holding no
// decoder — the factory's fresh box, and therefore the pool's floor, since
// sync.Pool only reaches its New function once the private slot, every P's
// shared queue and the victim cache are all exhausted.
//
// It is the difference between a test that exercises the branch it names and
// one that only might: a Put lands in the current P's PRIVATE slot only when
// that slot is free, so priming a pool that already holds a box files the
// primed one behind it and the next take gets the wrong one entirely. Emptying
// first makes the following Put-then-take a certainty rather than a race.
func emptyPool(tb testing.TB, sc readerSchemeCase) {
	tb.Helper()
	//: the count is what hitDecode attributes a take by; here it is discarded.
	poolDepth(tb, sc)
}

// poolDepth empties sc's pool and reports how many RECYCLED boxes it held. It
// is how a take that refused the caller's stream is attributed: such a take
// hands back no box to compare a pointer against, because production has
// already put it away itself, so the count of what is left is the only witness.
func poolDepth(tb testing.TB, sc readerSchemeCase) int {
	tb.Helper()
	for depth := range poolDrainAttempts {
		//: a box with no decoder came from the factory, so the pool is dry.
		if sc.rawGet().rc == nil {
			//: every recycled box has been accounted for.
			return depth
		}
	}
	tb.Fatalf("%s: pool still not empty after %d takes", sc.name, poolDrainAttempts)
	return 0
}

// emptyWriterPool is emptyPool for the encoder side; every writer factory
// yields nil on a miss, which is the same floor signal.
func emptyWriterPool(tb testing.TB, wc writerSchemeCase) {
	tb.Helper()
	for range poolDrainAttempts {
		//: a nil writer came from the factory, so the pool is dry.
		if wc.rawGet() {
			//: nothing recycled is left to interfere with what follows.
			return
		}
	}
	tb.Fatalf("%s: writer pool still not empty after %d takes", wc.name, poolDrainAttempts)
}

// primedBox leaves sc's pool holding exactly one box, filled by the scheme's
// own take — nothing here reproduces a take's internal state by hand — and
// returns it so a later take can be recognised by pointer. Nothing is PROVEN
// here: the proof that a take actually reused the box belongs to hitDecode,
// which is where the measurement happens.
func primedBox(tb testing.TB, sc readerSchemeCase) *readerBox {
	tb.Helper()
	box := takeAndDrain(tb, sc, sc.good)
	emptyPool(tb, sc)
	sc.release(box)
	//: the pool now holds this box and nothing else.
	return box
}

// takeAndDrain runs one full take-drain-release cycle over wire and fails the
// test if the scheme refuses it, which is the fixture path where wire is known
// good.
func takeAndDrain(tb testing.TB, sc readerSchemeCase, wire []byte) *readerBox {
	tb.Helper()
	box, err := sc.take(bytes.NewReader(wire))
	//: the fixture frame is valid by construction; a refusal is a broken test.
	if err != nil {
		tb.Fatalf("%s: seeding take err=%v", sc.name, err)
	}
	//: drain to EOF so the decoder is in the state production recycles.
	if _, derr := drainReader(box.rc); derr != nil {
		tb.Fatalf("%s: seeding drain err=%v", sc.name, derr)
	}
	//: hand the box back still taken; the caller owns the release.
	return box
}

// poolFrame encodes poolTestPayload through compress. The fixtures come from
// the schemes themselves rather than from hard-coded bytes, so a frame can
// never drift from the encoder that has to read it back.
func poolFrame(tb testing.TB, name string, compress func(dst, src []byte) (encoded []byte, err error)) []byte {
	tb.Helper()
	frame, err := compress(nil, []byte(poolTestPayload))
	//: a scheme that cannot encode the fixture makes every row below vacuous.
	if err != nil {
		tb.Fatalf("%s: fixture Compress err=%v", name, err)
	}
	//: a frame this scheme decodes back to poolTestPayload.
	return frame
}

// gzipReaderCase builds the gzip row of the decoder table.
func gzipReaderCase(tb testing.TB) readerSchemeCase {
	tb.Helper()
	return readerSchemeCase{
		name: "gzip", tag: schemeGzip,
		take: takeGzipReader, release: releaseGzipReader,
		rawGet: func() *readerBox { return gzipReaderPool.Get() },
		fresh: func(src io.Reader) (io.ReadCloser, error) {
			r, nerr := gzip.NewReader(src)
			//: a refused header yields no reader; keep the interface nil.
			if nerr != nil {
				//: the constructor's verdict is the miss path's verdict.
				return nil, nerr
			}
			//: hand the concrete decoder back through the shared interface.
			return r, nil
		},
		good: poolFrame(tb, "gzip", gzipCompressor{}.Compress),
		//: gzip's magic is two fixed bytes, so any non-frame refuses at once.
		bad:        []byte("this is emphatically not an RFC 1952 header"),
		decompress: gzipDecompressAtCeiling,
		code:       CodeGzipFailed,
	}
}

// flateReaderCase builds the flate row of the decoder table.
func flateReaderCase(tb testing.TB) readerSchemeCase {
	tb.Helper()
	return readerSchemeCase{
		name: "flate", tag: schemeFlate,
		take: takeFlateReader, release: releaseFlateReader,
		rawGet: func() *readerBox { return flateReaderPool.Get() },
		fresh: func(src io.Reader) (io.ReadCloser, error) {
			//: raw DEFLATE has no header, so this constructor cannot fail.
			return flate.NewReader(src), nil
		},
		good: poolFrame(tb, "flate", flateCompressor{}.Compress),
		//: the refusal fixture is the SIBLING envelope, not random bytes: an
		//: RFC 1950 frame handed to the raw-DEFLATE scheme is the confusion
		//: this package's CLAUDE.md calls its most likely interop bug, and
		//: measured, a flate decoder reads 0x78 0x9c as a corrupt block and
		//: fails "flate: corrupt input before offset 5".
		bad:        poolFrame(tb, "zlib", zlibCompressor{level: zlib.DefaultCompression}.Compress),
		decompress: flateDecompressAtCeiling,
		code:       CodeFlateFailed,
	}
}

// zlibReaderCase builds the zlib row of the decoder table.
func zlibReaderCase(tb testing.TB) readerSchemeCase {
	tb.Helper()
	return readerSchemeCase{
		name: "zlib", tag: schemeZlib,
		take: takeZlibReader, release: releaseZlibReader,
		rawGet: func() *readerBox { return zlibReaderPool.Get() },
		fresh:  zlib.NewReader,
		good:   poolFrame(tb, "zlib", zlibCompressor{level: zlib.DefaultCompression}.Compress),
		//: the refusal fixture is a RAW DEFLATE frame, which is the sharpest
		//: one available and the reason it is used rather than random bytes:
		//: measured, zlib.NewReader refuses it "zlib: invalid header" while a
		//: flate decoder decodes it back to poolTestPayload exactly. So a zlib
		//: decode that ever runs on the raw-DEFLATE grammar — the crossing the
		//: scheme tag exists to stop — ACCEPTS this frame instead of refusing
		//: it, and says so here rather than anywhere subtler.
		bad:        poolFrame(tb, "flate", flateCompressor{}.Compress),
		decompress: zlibDecompressAtCeiling,
		code:       CodeZlibFailed,
	}
}

// gzipDecompressAtCeiling is the gzip scheme's package-edge decode, named so the
// table can hold a plain func value rather than a method value.
func gzipDecompressAtCeiling(dst, src []byte) (decoded []byte, err error) {
	//: the production ceiling, exactly as gzipCompressor.Decompress passes it.
	return gzipDecompress(dst, src, maxDecompressedBytes)
}

// flateDecompressAtCeiling is the flate scheme's package-edge decode.
func flateDecompressAtCeiling(dst, src []byte) (decoded []byte, err error) {
	//: the production ceiling, exactly as flateCompressor.Decompress passes it.
	return flateDecompress(dst, src, maxDecompressedBytes)
}

// zlibDecompressAtCeiling is the zlib scheme's package-edge decode.
func zlibDecompressAtCeiling(dst, src []byte) (decoded []byte, err error) {
	//: the production ceiling, exactly as zlibCompressor.Decompress passes it.
	return zlibDecompress(dst, src, maxDecompressedBytes)
}

// readerSchemeCases builds the three decoder rows in one call.
func readerSchemeCases(tb testing.TB) []readerSchemeCase {
	tb.Helper()
	//: one row per scheme; every reader test walks all three.
	return []readerSchemeCase{gzipReaderCase(tb), flateReaderCase(tb), zlibReaderCase(tb)}
}

// writeAndClose encodes src through w and flushes it, failing the test on
// either fault; both are buffer-backed here, so either is a broken fixture.
func writeAndClose(tb testing.TB, w io.WriteCloser, src []byte) {
	tb.Helper()
	//: a Write into a bytes.Buffer cannot fail short of a broken encoder.
	if _, err := w.Write(src); err != nil {
		tb.Fatalf("encoder Write err=%v", err)
	}
	//: Close flushes the trailer and is the authoritative encode result.
	if err := w.Close(); err != nil {
		tb.Fatalf("encoder Close err=%v", err)
	}
}

// gzipWriterCase builds the gzip row of the encoder table.
func gzipWriterCase() writerSchemeCase {
	return writerSchemeCase{
		name: "gzip",
		cycle: func(tb testing.TB, src []byte) (wire []byte, identity any) {
			tb.Helper()
			var buf bytes.Buffer
			w := takeGzipWriter(&buf)
			defer releaseGzipWriter(w)
			writeAndClose(tb, w, src)
			//: the encoder pointer is the identity a reuse is proven by.
			return buf.Bytes(), w
		},
		fresh: func(tb testing.TB, src []byte) (wire []byte) {
			tb.Helper()
			var buf bytes.Buffer
			//: gzip.NewWriter is verbatim what takeGzipWriter's miss branch calls.
			writeAndClose(tb, gzip.NewWriter(&buf), src)
			return buf.Bytes()
		},
		rawGet: func() (empty bool) {
			//: the factory yields nil, so nil means the pool is dry.
			return gzipWriterPool.Get() == nil
		},
	}
}

// flateWriterCase builds the flate row of the encoder table.
func flateWriterCase() writerSchemeCase {
	return writerSchemeCase{
		name: "flate",
		cycle: func(tb testing.TB, src []byte) (wire []byte, identity any) {
			tb.Helper()
			var buf bytes.Buffer
			w, err := takeFlateWriter(&buf)
			//: DefaultCompression is in band, so this take cannot refuse.
			if err != nil {
				tb.Fatalf("flate: takeFlateWriter err=%v", err)
			}
			defer releaseFlateWriter(w)
			writeAndClose(tb, w, src)
			//: the encoder pointer is the identity a reuse is proven by.
			return buf.Bytes(), w
		},
		fresh: func(tb testing.TB, src []byte) (wire []byte) {
			tb.Helper()
			var buf bytes.Buffer
			w, err := flate.NewWriter(&buf, flate.DefaultCompression)
			//: flate.NewWriter is verbatim what takeFlateWriter's miss branch calls.
			if err != nil {
				tb.Fatalf("flate: NewWriter err=%v", err)
			}
			writeAndClose(tb, w, src)
			return buf.Bytes()
		},
		rawGet: func() (empty bool) {
			//: the factory yields nil, so nil means the pool is dry.
			return flateWriterPool.Get() == nil
		},
	}
}

// zlibWriterCase builds one zlib row of the encoder table at the given level.
// level is a parameter rather than a captured loop variable, so the closures
// below hold a copy and nothing escapes per iteration.
func zlibWriterCase(name string, level int) writerSchemeCase {
	return writerSchemeCase{
		name: name,
		cycle: func(tb testing.TB, src []byte) (wire []byte, identity any) {
			tb.Helper()
			var buf bytes.Buffer
			w, err := takeZlibWriter(&buf, level)
			//: every level in this table is in band, so the take cannot refuse.
			if err != nil {
				tb.Fatalf("%s: takeZlibWriter err=%v", name, err)
			}
			defer releaseZlibWriter(w, level)
			writeAndClose(tb, w, src)
			//: the encoder pointer is the identity a reuse is proven by.
			return buf.Bytes(), w
		},
		fresh: func(tb testing.TB, src []byte) (wire []byte) {
			tb.Helper()
			var buf bytes.Buffer
			w, err := zlib.NewWriterLevel(&buf, level)
			//: NewWriterLevel is verbatim what takeZlibWriter's miss branch calls.
			if err != nil {
				tb.Fatalf("%s: NewWriterLevel err=%v", name, err)
			}
			writeAndClose(tb, w, src)
			return buf.Bytes()
		},
		rawGet: func() (empty bool) {
			//: this level's own pool; the factory yields nil on a miss.
			return zlibWriterPoolFor(level).Get() == nil
		},
	}
}

// writerSchemeCases builds the encoder rows: one per scheme, plus both ends of
// the zlib level band, since zlib is the only scheme whose pools are KEYED by
// level and therefore the only one where a reuse could cross levels.
func writerSchemeCases() []writerSchemeCase {
	//: HuffmanOnly and BestCompression are the band's two extremes.
	return []writerSchemeCase{
		gzipWriterCase(),
		flateWriterCase(),
		zlibWriterCase("zlib default", zlib.DefaultCompression),
		zlibWriterCase("zlib HuffmanOnly", zlib.HuffmanOnly),
		zlibWriterCase("zlib BestCompression", zlib.BestCompression),
	}
}

// proveWriterReuse runs wc's pooled cycle until two consecutive cycles use the
// SAME encoder, and returns the bytes the second — provably recycled — cycle
// produced. Encoder identity is the only observable proof that the take's Reset
// branch ran rather than its constructor branch.
func proveWriterReuse(tb testing.TB, wc writerSchemeCase, src []byte) (wire []byte) {
	tb.Helper()
	emptyWriterPool(tb, wc)
	_, previous := wc.cycle(tb, src)
	for range poolHitAttempts {
		encoded, identity := wc.cycle(tb, src)
		//: the same encoder twice means this cycle took the Reset path.
		if identity == previous {
			//: these are the recycled encoder's own bytes.
			return encoded
		}
		//: a different encoder means the private slot was lost; retry from it.
		previous = identity
	}
	tb.Fatalf("%s: no encoder reuse in %d attempts", wc.name, poolHitAttempts)
	return nil
}

// TestReaderPoolReuseDecodesIdenticallyToAFreshDecoder is the pin for the whole
// point of pool.go: a recycled decoder must produce the byte-identical
// plaintext a freshly constructed one produces. The reference is the scheme's
// own cold-path constructor, so the comparison is reuse against construction
// and nothing else, and the hit is proven by pointer identity rather than
// hoped for.
//
// PROVEN TO BITE. Deleting the Reset call from takeFlateReader's reuse branch
// — `_ = resetter` plus `if rerr := error(nil)`, so the recycled decoder stays
// bound to the stream it read last — failed as
// `flate: reuse decoded 0 bytes, fresh decoded 109`, and the same deletion in
// takeGzipReader (`_ = decoder`) failed as
// `gzip: reuse decoded 0 bytes, fresh decoded 109`. Zero is what a recycled
// decoder returns when nothing rebound it: it is still parked at the end of the
// frame it had already drained, so it reports a clean EOF and no error at all.
// That is the shape of the regression worth fearing here — not a crash, an
// empty answer.
func TestReaderPoolReuseDecodesIdenticallyToAFreshDecoder(t *testing.T) {
	tests := readerSchemeCases(t)
	//: runCase executes one row directly so the static analyser credits it.
	runCase := func(t *testing.T, sc readerSchemeCase) {
		t.Helper()
		want, werr := drainFresh(sc, sc.good)
		//: the miss reference must succeed, or the fixture frame is broken.
		if werr != nil {
			t.Fatalf("%s: fresh decoder err=%v", sc.name, werr)
		}
		box := primedBox(t, sc)
		plain, derr := hitDecode(t, sc, box, sc.good)
		//: a good frame is never refused, and a recycled decoder that drops or
		//: truncates bytes fails here.
		if derr != nil {
			t.Fatalf("%s: reuse decode err=%v", sc.name, derr)
		}
		//: byte-identical to construction is the whole claim.
		if !bytes.Equal(plain, want) {
			t.Errorf("%s: reuse decoded %d bytes, fresh decoded %d", sc.name, len(plain), len(want))
		}
		//: and both must actually be the payload, not two identical mistakes.
		if string(plain) != poolTestPayload {
			t.Errorf("%s: reuse decoded %q", sc.name, plain)
		}
	}
	for _, sc := range tests {
		t.Run(sc.name, func(t *testing.T) {
			runCase(t, sc)
		})
	}
}

// TestWriterPoolReuseEncodesIdenticallyToAFreshEncoder is the encoder half of
// the same claim, and covers the one thing only the encoder side can get wrong:
// the zlib pools are KEYED BY LEVEL, so both ends of the band are walked. A
// recycled writer that carried its history window, its level, or a written
// header across the Reset would produce different bytes here.
//
// PROVEN TO BITE. Replacing takeZlibWriter's `w.Reset(dst)` with `_ = dst`, so
// the recycled encoder stayed bound to the PREVIOUS call's buffer, failed every
// zlib row at once: `reused encoder produced` nothing at all where a fresh
// encoder produced 122, 122 and 77 for default, HuffmanOnly and
// BestCompression, and the same mutation in takeGzipWriter left the gzip row
// empty against a fresh 134. The caller's buffer is not corrupted by this bug;
// it is simply never written, which is again an empty answer rather than a
// crash — the same shape as the decoder half above.
func TestWriterPoolReuseEncodesIdenticallyToAFreshEncoder(t *testing.T) {
	tests := writerSchemeCases()
	//: runCase executes one row directly so the static analyser credits it.
	runCase := func(t *testing.T, wc writerSchemeCase) {
		t.Helper()
		payload := []byte(poolTestPayload)
		want := wc.fresh(t, payload)
		got := proveWriterReuse(t, wc, payload)
		//: byte-identical to a freshly constructed encoder is the whole claim.
		if !bytes.Equal(got, want) {
			t.Errorf("%s: reused encoder produced %d bytes, fresh produced %d", wc.name, len(got), len(want))
		}
	}
	for _, wc := range tests {
		t.Run(wc.name, func(t *testing.T) {
			runCase(t, wc)
		})
	}
}

// TestReaderPoolRefusesMalformedIdenticallyOnHitAndMiss is a security pin, not
// a performance one: a header re-validated only when the pool misses would make
// a stream's acceptance depend on how busy the process was. Both the verdict
// and the error text are compared, against the cold path's own constructor.
//
// The take that runs the malformed frame is confirmed to have been a HIT after
// the fact: the box the take was primed with must come back out of the pool on
// the very next take, which it can only do if that take used it.
//
// PROVEN TO BITE, on the security line itself. Deleting the reuse branch's
// Reset from takeFlateReader, so the recycled decoder never re-read the
// caller's stream, failed as
// `flate: hit path accepted the malformed frame that the miss path refused`;
// the same deletion in takeGzipReader failed identically,
// `gzip: hit path accepted the malformed frame that the miss path refused`. In
// both the fresh decoder refused the frame and the recycled one returned a
// clean empty read, which is acceptance.
//
// One mutation deliberately does NOT bite, and it is worth recording so nobody
// re-adds a guard for it: merely DISCARDING the Reset verdict
// (`_ = resetter.Reset(src, nil)`) changes nothing observable, because
// gzip.Reader.Reset and zlib.reader.Reset both LATCH the header error into the
// decoder, so the very next Read returns it and the drain produces the
// byte-identical message. The verdict check is a convenience, not the
// validation; the validation is the Reset call.
func TestReaderPoolRefusesMalformedIdenticallyOnHitAndMiss(t *testing.T) {
	tests := readerSchemeCases(t)
	//: runCase executes one row directly so the static analyser credits it.
	runCase := func(t *testing.T, sc readerSchemeCase) {
		t.Helper()
		//: the miss reference is the cold path's own constructor, used direct.
		_, missErr := drainFresh(sc, sc.bad)
		//: a fixture the miss path accepts would make the comparison vacuous.
		if missErr == nil {
			t.Fatalf("%s: miss path accepted the malformed frame", sc.name)
		}
		box := primedBox(t, sc)
		hitPlain, hitErr := hitDecode(t, sc, box, sc.bad)
		//: the reuse path must refuse what construction refuses.
		if hitErr == nil {
			t.Fatalf("%s: hit path accepted the malformed frame that the miss path refused", sc.name)
		}
		//: and refuse it for the same stated reason, not merely refuse it.
		if hitErr.Error() != missErr.Error() {
			t.Errorf("%s: hit err=%q miss err=%q", sc.name, hitErr, missErr)
		}
		//: a refusal must yield no plaintext at all.
		if len(hitPlain) != 0 {
			t.Errorf("%s: refused frame still produced %d bytes", sc.name, len(hitPlain))
		}
		assertStillDecodes(t, sc)
	}
	for _, sc := range tests {
		t.Run(sc.name, func(t *testing.T) {
			runCase(t, sc)
		})
	}
}

// hitDecode runs one take over wire against a pool holding exactly box, and
// returns the verdict — the take refusal and the drain refusal folded into one,
// which is the same fold gzipDecompress, flateDecompress and zlibDecompress
// each perform. Raw DEFLATE has no header, so for the flate scheme the refusal
// can only arrive at the drain; folding is what lets one assertion cover all
// three.
//
// The take is ATTRIBUTED before its verdict is used, and by pool DEPTH rather
// than by pointer, because a take that refuses the stream hands back no box to
// compare: production has already put it away. Emptying first leaves box as the
// pool's only entry, so afterwards a depth of one means the take took box and
// put it back, while a depth of two means it missed, served a box of its own,
// and left box where it lay. This goroutine migrating to another P between the
// Put and the take's Get is the one thing that produces the latter — rare,
// invisible, and observed under -race — so it is retried rather than asserted
// away, and an unattributable run never becomes a quietly weaker assertion.
func hitDecode(tb testing.TB, sc readerSchemeCase, box *readerBox, wire []byte) (plain []byte, err error) {
	tb.Helper()
	for range poolHitAttempts {
		emptyPool(tb, sc)
		sc.release(box)
		got, terr := sc.take(bytes.NewReader(wire))
		plain, err = nil, terr
		//: a take that accepted the stream owns a box the caller must return.
		if terr == nil {
			plain, err = drainReader(got.rc)
			sc.release(got)
		}
		depth := poolDepth(tb, sc)
		sc.release(box)
		//: exactly one recycled box survived, so the take ran on box.
		if depth == 1 {
			//: attributed — this verdict is the reuse path's own.
			return plain, err
		}
	}
	tb.Fatalf("%s: no attributable pool hit in %d attempts", sc.name, poolHitAttempts)
	return nil, nil
}

// assertStillDecodes checks that a refusal left nothing broken behind it: the
// very next decode on the same pool must still produce the payload.
func assertStillDecodes(tb testing.TB, sc readerSchemeCase) {
	tb.Helper()
	plain, err := sc.decompress(nil, sc.good)
	//: a refusal that wedged the pool shows up on the decode after it.
	if err != nil {
		tb.Fatalf("%s: decode after refusal err=%v", sc.name, err)
	}
	//: and the recovered decode must still be the payload.
	if string(plain) != poolTestPayload {
		tb.Errorf("%s: after refusal decoded %q", sc.name, plain)
	}
}

// corruptCase is one wrongly-filled pool: a scheme, and a box that does not
// belong in its pool.
type corruptCase struct {
	// name identifies the pool and what was dropped into it.
	name string
	// sc is the scheme whose pool receives the foreign box.
	sc readerSchemeCase
	// build produces the foreign box, fresh per run.
	build func(tb testing.TB) *readerBox
}

// alienReadCloser is a decoder-shaped value with no Reset method at all, so it
// fails the capability half of a take's condition rather than the scheme half.
// io.NopCloser is deliberately not used: what it returns varies with whether
// the wrapped reader implements io.WriterTo, and this fixture must not.
type alienReadCloser struct{}

// Read reports end of stream immediately; nothing here ever drains it.
func (alienReadCloser) Read(_ []byte) (n int, err error) {
	//: an empty stream is enough — the value exists to fail an assertion.
	return 0, io.EOF
}

// Close is a no-op, present only to satisfy io.ReadCloser.
func (alienReadCloser) Close() error {
	//: nothing to release.
	return nil
}

// crossCase builds the row where other's decoder is dropped into sc's pool.
// Both arguments are parameters rather than captured loop variables, so each
// row's closure holds its own copy.
func crossCase(sc, other readerSchemeCase) corruptCase {
	return corruptCase{
		name: sc.name + " pool holding a " + other.name + " decoder",
		sc:   sc,
		build: func(tb testing.TB) *readerBox {
			tb.Helper()
			//: a box the OTHER scheme's own take filled, so rc and the scheme
			//: tag agree with each other and disagree with the pool — exactly
			//: the shape a mispaired release produces.
			return takeAndDrain(tb, other, other.good)
		},
	}
}

// alienCase builds the row where a decoder with no Reset at all is dropped into
// sc's pool, which is the other half of the take's condition.
func alienCase(sc readerSchemeCase) corruptCase {
	return corruptCase{
		name: sc.name + " pool holding a decoder with no Reset",
		sc:   sc,
		build: func(tb testing.TB) *readerBox {
			tb.Helper()
			//: schemeNone plus a Reset-less decoder fails both halves at once.
			return &readerBox{rc: alienReadCloser{}, scheme: schemeNone}
		},
	}
}

// corruptionCases enumerates every wrongly-filled pool this package can produce
// from its own parts: each scheme's pool holding each other scheme's decoder,
// plus each scheme's pool holding a decoder with no Reset.
func corruptionCases(tb testing.TB) []corruptCase {
	tb.Helper()
	schemes := readerSchemeCases(tb)
	cases := make([]corruptCase, 0, len(schemes)*len(schemes))
	for _, sc := range schemes {
		for _, other := range schemes {
			//: a box from its own pool is not a corruption.
			if other.tag == sc.tag {
				continue
			}
			cases = append(cases, crossCase(sc, other))
		}
		cases = append(cases, alienCase(sc))
	}
	//: one row per (pool, foreign decoder) pair.
	return cases
}

// corruptPool leaves cc's foreign box as the ONLY thing in the scheme's pool,
// so the next take is certain to find it. Without the emptying step the primed
// box files behind whatever already held the private slot and the take reaches
// a perfectly good decoder instead — which is how a test can name a branch it
// never runs. The box is handed over through the production release, because a
// mispaired release is the only way this corruption could ever arise.
func corruptPool(tb testing.TB, cc corruptCase) {
	tb.Helper()
	foreign := cc.build(tb)
	emptyPool(tb, cc.sc)
	cc.sc.release(foreign)
}

// TestReaderPoolHealsAForeignDecoder drives the corrupted-pool branch that the
// interface assertion alone could not reach. Before the scheme tag, a zlib
// decoder in the flate pool passed flate.Resetter, parsed an RFC 1950 header
// out of a raw DEFLATE stream, failed "zlib: invalid header" under FlateFailed,
// and — because the assertion had passed — went straight back into the pool, so
// every later flate decode on that P failed the same way. The reverse crossing
// was quieter: a flate decoder in the zlib pool ACCEPTED the Reset and skipped
// the header check entirely.
//
// So the assertion is not merely "does not panic": the scheme must round-trip
// on the very next call, and keep round-tripping, and still refuse a malformed
// frame — the header check must not have been skipped.
//
// PROVEN TO BITE, and this is the mutation that matters most, because it is
// the code as it stood before this change: reverting BOTH guards to the bare
// interface assertion (`if !pooled {`) failed as
// `flate pool holding a zlib decoder: malformed frame err=<nil>, want code 0.3.26.2`
// and
// `zlib pool holding a flate decoder: malformed frame err=<nil>, want code 0.3.26.3`.
// `err=<nil>` is the whole finding: each scheme ACCEPTED the sibling envelope
// it exists to refuse, silently and with a correct-looking payload.
func TestReaderPoolHealsAForeignDecoder(t *testing.T) {
	tests := corruptionCases(t)
	//: runCase executes one row directly so the static analyser credits it.
	runCase := func(t *testing.T, cc corruptCase) {
		t.Helper()
		//: FIRST corruption — the take that finds the foreign decoder is the
		//: one judging a frame this scheme must refuse. This is the security
		//: half: for zlib the refusal fixture is a raw DEFLATE frame, so a
		//: decode that ran on the foreign grammar would ACCEPT it.
		corruptPool(t, cc)
		if _, err := cc.sc.decompress(nil, cc.sc.bad); !errs.HasCode(err, cc.sc.code) {
			t.Fatalf("%s: malformed frame err=%v, want code %v", cc.name, err, cc.sc.code)
		}
		//: SECOND corruption — this time the take that finds the foreign
		//: decoder is judging a good frame. This is the healing half, and it
		//: runs more than once because healing that lasts only for the take
		//: which found the foreign decoder is not healing.
		corruptPool(t, cc)
		for round := range poolHealRounds {
			plain, err := cc.sc.decompress(nil, cc.sc.good)
			//: a corrupted pool is this package's defect, never the caller's
			//: error, so the round-trip must simply succeed.
			if err != nil {
				t.Fatalf("%s: round %d decompress err=%v", cc.name, round, err)
			}
			//: and produce the payload, not another scheme's reading of it.
			if string(plain) != poolTestPayload {
				t.Fatalf("%s: round %d decoded %q", cc.name, round, plain)
			}
		}
	}
	for _, cc := range tests {
		t.Run(cc.name, func(t *testing.T) {
			runCase(t, cc)
		})
	}
}

// TestReaderPoolIsNotWedgedByARefusedStream alternates a refusal and a
// legitimate decode, because the two paths that handle a refusal put the box
// back differently — a construction failure empties it, a Reset failure keeps
// its decoder — and either one leaving the pool in a state the next take cannot
// use would turn one bad request into a permanently broken scheme.
//
// PROVEN TO BITE by reproducing the historical fault whole: the bare interface
// assertion (`if !pooled {`) plus one mispaired release — releaseFlateReader
// putting into zlibReaderPool — failed as
// `zlib: seeding drain err=flate: corrupt input before offset 5`. That is the
// QUIETER of the two crossings: the flate decoder accepted the zlib pool's
// Reset without complaint, skipping the RFC 1950 header check entirely, and the
// stream was then judged by the raw-DEFLATE grammar, so the failure surfaced at
// the drain wearing the wrong scheme's words. Diverting the other way
// (releaseZlibReader into flateReaderPool) failed the neighbouring tests as
// `zlib: no attributable pool hit in 64 attempts`, the scheme whose releases
// were stolen having nothing left to recycle.
func TestReaderPoolIsNotWedgedByARefusedStream(t *testing.T) {
	tests := readerSchemeCases(t)
	//: runCase executes one row directly so the static analyser credits it.
	runCase := func(t *testing.T, sc readerSchemeCase) {
		t.Helper()
		//: start from a pool that is HOT and holds exactly one filled box, so
		//: the refusals below land on the reuse path and not only on the
		//: constructor path.
		primedBox(t, sc)
		for round := range poolWedgeRounds {
			//: a refused frame carries this scheme's own code...
			if _, err := sc.decompress(nil, sc.bad); !errs.HasCode(err, sc.code) {
				t.Fatalf("%s: round %d refusal err=%v, want code %v", sc.name, round, err, sc.code)
			}
			//: ...and must leave the pool able to serve the very next decode.
			plain, err := sc.decompress(nil, sc.good)
			//: a wedged pool shows up here, on the round after the refusal.
			if err != nil {
				t.Fatalf("%s: round %d decode after refusal err=%v", sc.name, round, err)
			}
			//: and the recovered decode must still be the payload.
			if string(plain) != poolTestPayload {
				t.Fatalf("%s: round %d decoded %q", sc.name, round, plain)
			}
		}
	}
	for _, sc := range tests {
		t.Run(sc.name, func(t *testing.T) {
			runCase(t, sc)
		})
	}
}

// TestZlibWriterPoolMatchesStdlibLevelBand pins the equivalence that lets
// releaseZlibWriter carry no nil-pool branch at all: zlibWriterPoolFor reports
// "no pool" for exactly the levels zlib.NewWriterLevel refuses, so a level with
// no pool is a level takeZlibWriter returns an error for, so the zlib scheme
// never registers the defer that would call releaseZlibWriter with one.
//
// The sweep is what makes the removal safe rather than merely tidy: moving
// either side of the band without the other fails here, at the invariant,
// instead of at a nil dereference in production.
//
// PROVEN TO BITE. Narrowing zlibWriterPoolFor's upper bound by one
// (`level > zlib.BestCompression-1`) failed as
// `level 9: pooled=false but zlib.NewWriterLevel err=<nil>` — a level the
// stdlib builds happily and the pool no longer keys, which is precisely the
// state in which releaseZlibWriter would meet a nil pool.
func TestZlibWriterPoolMatchesStdlibLevelBand(t *testing.T) {
	type tc struct {
		// name identifies the level under test.
		name string
		// level is the compression level probed on both sides.
		level int
	}
	tests := make([]tc, 0, poolLevelSweepHigh-poolLevelSweepLow+1)
	for level := poolLevelSweepLow; level <= poolLevelSweepHigh; level++ {
		tests = append(tests, tc{name: "level " + strconv.Itoa(level), level: level})
	}
	//: runCase executes one row directly so the static analyser credits it.
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, stdErr := zlib.NewWriterLevel(io.Discard, c.level)
		pooled := zlibWriterPoolFor(c.level) != nil
		//: the two conditions must be one condition, or the removed branch
		//: stops being unreachable.
		if pooled != (stdErr == nil) {
			t.Fatalf("%s: pooled=%v but zlib.NewWriterLevel err=%v", c.name, pooled, stdErr)
		}
		assertZlibLevelPairing(t, c.name, c.level, pooled)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// assertZlibLevelPairing checks the take/release pairing for one level: an
// unpooled level must be refused by the take, which is precisely why
// releaseZlibWriter never runs for it; a pooled level must survive a full
// take-and-release cycle, which is the only place releaseZlibWriter is reached.
func assertZlibLevelPairing(tb testing.TB, name string, level int, pooled bool) {
	tb.Helper()
	w, err := takeZlibWriter(io.Discard, level)
	//: no pool means the take must refuse, so the caller never defers a
	//: release — the whole reason the nil-pool branch could be removed.
	if !pooled {
		//: a take that accepted an unpooled level would reach a nil pool.
		if err == nil {
			tb.Fatalf("%s: no pool, yet takeZlibWriter accepted the level", name)
		}
		//: a refused take must hand back no writer to release.
		if w != nil {
			tb.Errorf("%s: refused take returned a non-nil writer", name)
		}
		return
	}
	//: a pooled level must round-trip through the pair without faulting.
	if err != nil {
		tb.Fatalf("%s: pooled level refused by takeZlibWriter: %v", name, err)
	}
	releaseZlibWriter(w, level)
}

// injectedResetFault is the error failingFlateResetter returns. It is a TYPE
// rather than a package-level sentinel so the fixture needs no var and no
// errors.New, and it stays comparable so errors.Is matches it directly.
type injectedResetFault struct{}

// Error renders the fault; nothing parses it, only errors.Is compares it.
func (injectedResetFault) Error() string {
	//: a name a failure message can be read against.
	return "transform test: injected flate Reset fault"
}

// failingFlateResetter satisfies flate.Resetter and always fails its Reset. It
// exists to reach the one branch in pool.go today's standard library cannot:
// flate's own Reset ends in `return nil`, so takeFlateReader's Reset-fault path
// is unreachable through any real stream. Production keeps that path so a
// future implementation which CAN fail fails typed rather than silently, and
// SDK rule 12 asks that a branch kept for the future have a gate that runs now.
type failingFlateResetter struct{}

// Reset always fails, which is the whole purpose of the type.
func (failingFlateResetter) Reset(_ io.Reader, _ []byte) error {
	//: the fault this fixture exists to inject.
	return injectedResetFault{}
}

// Read is never reached — the take returns before anything drains this.
func (failingFlateResetter) Read(_ []byte) (n int, err error) {
	//: unreachable in this fixture's use, present to satisfy io.ReadCloser.
	return 0, io.EOF
}

// Close is never reached either, for the same reason.
func (failingFlateResetter) Close() error {
	//: nothing to release.
	return nil
}

// TestFlateReaderPoolSurfacesAResetFault gates takeFlateReader's Reset-fault
// branch — the one statement group in pool.go that no real stream can reach,
// because compress/flate's Reset cannot fail. Production keeps it so that an
// implementation which one day CAN fail fails TYPED, and a kept branch with no
// gate is a branch nothing ever runs.
//
// It is deliberately the only test in this file that fabricates a box state
// production cannot produce — a flate scheme tag over a decoder no take would
// have put there — which is precisely what lets it reach the branch. What it
// asserts is the branch's contract and not its existence: the fault travels
// back rather than being discarded, no box is handed to a caller who cannot
// drain it, the box goes BACK to the pool instead of being dropped, and the
// scheme keeps working afterwards.
//
// PROVEN TO BITE, on both halves, each as
// `the Reset fault never came back with its box in 64 attempts`: swallowing the
// Reset verdict in takeFlateReader (running the Reset but returning nil in its
// place) makes the branch report success for a decoder that had just refused to
// reinitialise, and deleting its `flateReaderPool.Put(held)` makes the branch
// drop the box on the floor. Neither can produce the attributed outcome, so
// both exhaust the loop rather than passing quietly.
func TestFlateReaderPoolSurfacesAResetFault(t *testing.T) {
	type tc struct {
		// name identifies the row in failure messages.
		name string
	}
	tests := []tc{{"a Reset fault travels back typed and recycles the box"}}
	//: runCase executes one row directly so the static analyser credits it.
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sc := flateReaderCase(t)
		for range poolHitAttempts {
			emptyPool(t, sc)
			sc.release(&readerBox{rc: failingFlateResetter{}, scheme: schemeFlate})
			box, err := takeFlateReader(bytes.NewReader(sc.good))
			//: the fault is its own attribution: no other box in this pool can
			//: produce it, so an err that is NOT it means the take missed the
			//: injected box entirely — a retry, never a verdict.
			if !errors.Is(err, injectedResetFault{}) {
				continue
			}
			//: a failed take hands back no box, since nothing may drain it.
			if box != nil {
				t.Errorf("%s: a failed take returned a box", c.name)
			}
			//: the box must be back in the pool rather than dropped on the
			//: floor. A GC between production's Put and this count can lose it
			//: to the victim cache, so an unattributable count is retried; a
			//: production path that stopped recycling the box would never
			//: produce the count and would exhaust the loop below.
			if poolDepth(t, sc) != 1 {
				continue
			}
			assertStillDecodes(t, sc)
			return
		}
		t.Fatalf("%s: the Reset fault never came back with its box in %d attempts", c.name, poolHitAttempts)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
