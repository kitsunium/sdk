// Package codec_test — fuzz coverage for the compressed-frame verbs. The target
// drives two legs from one corpus entry: raw bytes through UnmarshalCompressed
// (the attacker-controlled path) and a fuzzed value through the
// MarshalCompressed → UnmarshalCompressed round trip (the success path, which
// raw bytes alone never reach because a random buffer is never a valid frame).
package codec_test

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/binary"
	"io"
	"testing"
	"unicode/utf8"

	codec "github.com/kitsunium/sdk/pkg/v1/codec"
	errs "github.com/kitsunium/sdk/pkg/v1/errs"
)

// Frozen wire constants mirrored from compressed.go. They are unexported there,
// so the black-box target restates them: a divergence between these literals and
// the production constants IS the regression the target exists to catch.
const (
	// fuzzFrameMagic is the sentinel first byte of every compressed frame.
	fuzzFrameMagic byte = 0xC7
	// fuzzFrameVersion is the only frame layout version this target accepts.
	fuzzFrameVersion byte = 0x01
	// fuzzAlgGzip is the frozen frame id for the gzip compressor.
	fuzzAlgGzip byte = 0x01
	// fuzzAlgFlate is the frozen frame id for the raw-DEFLATE compressor.
	fuzzAlgFlate byte = 0x02
	// fuzzFrameHeaderLen is magic + version + algID + a 2-byte length field.
	fuzzFrameHeaderLen int = 5
	// fuzzFormatLenStart is the offset of the 2-byte inner-Format length field.
	fuzzFormatLenStart int = 3
)

// Bomb-guard thresholds mirrored from compressed.go, used by the converse
// assertion: on a structurally valid header, the bomb guard is the ONLY other
// producer of CompressedFrameInvalid, so the assertion must be able to recompute
// it rather than assume it.
const (
	// fuzzMaxExpansionRatio bounds decompressed/compressed size.
	fuzzMaxExpansionRatio int = 1000
	// fuzzBombFloorBytes is the size below which the ratio guard does not apply.
	fuzzBombFloorBytes int = 4 << 10
	// fuzzMaxDecompressedFrameBytes is the absolute frame-layer output ceiling.
	fuzzMaxDecompressedFrameBytes int = 64 << 20
)

// Reference-decompression outcomes. A plain (plain, bool) pair cannot tell "this
// stream is corrupt" from "this stream is larger than the frame ceiling", and
// the two lead to different verdicts in the converse assertion.
const (
	// fuzzDecompressFailed means the payload is not a valid stream for algo.
	fuzzDecompressFailed int = iota
	// fuzzDecompressOK means the payload decompressed within the frame ceiling.
	fuzzDecompressOK
	// fuzzDecompressOverCap means output exceeded the frame ceiling — a bomb by
	// construction, whatever the exact size.
	fuzzDecompressOverCap
)

// fuzzFrameInvalidReason is the single non-oracle rejection reason a
// structurally invalid frame is contractually allowed to carry.
const fuzzFrameInvalidReason string = "COMPRESSED_FRAME_INVALID"

// fuzzLeakReasons are the reasons a structurally invalid frame must NEVER carry.
// Each one would tell an attacker which sub-check failed, which is exactly the
// oracle compressed.go's doc contract refuses to give ("A malformed frame, an
// unknown algorithm id, or a payload that decompresses past the bomb guard
// returns CompressedFrameInvalid").
var fuzzLeakReasons = []string{
	"GZIP_FAILED",
	"FLATE_FAILED",
	"ZLIB_FAILED",
	"UNKNOWN_COMPRESSOR",
	"UNKNOWN_FORMAT",
	"UNMARSHAL_FAILED",
	"PROMOTE_FAILED",
}

// fuzzFormats is every Format the facade exposes. The frame layer is
// format-agnostic, so the interesting variable it exercises is the inner-Format
// NAME LENGTH written into the two-byte header field: the names in this table
// span from the shortest ("xml") to the longest ("flatbuffers"), which is the
// spread the length field actually sees in production.
var fuzzFormats = []codec.Format{
	codec.JSON, codec.NDJSON, codec.XML, codec.CSV, codec.Form,
	codec.ASN1DER, codec.PEM, codec.YAML, codec.TOML, codec.CBOR,
	codec.MsgPack, codec.TLV, codec.FlatBuffers, codec.Base64,
	codec.Base64URL, codec.Base32, codec.Base16, codec.Hex,
	codec.ASCII85, codec.Base45, codec.Base58, codec.Base62,
	codec.BSON, codec.Multipart,
}

// fuzzFrameView is the reference decode of a frame header: an independent
// transcription of parseFrame's structural rules, against which the production
// verdict is differentially checked.
type fuzzFrameView struct {
	algo    codec.CompressAlgorithm
	format  codec.Format
	payload []byte
	ok      bool
}

// fuzzParseFrame reimplements parseFrame's structural checks from the wire
// contract alone. It is deliberately a second implementation: asserting against
// the production function would make the invariant a tautology.
func fuzzParseFrame(box []byte) fuzzFrameView {
	//: the buffer must span the fixed header and carry the right magic/version.
	if len(box) < fuzzFrameHeaderLen || box[0] != fuzzFrameMagic || box[1] != fuzzFrameVersion {
		//: too short, or not a frame this version produced.
		return fuzzFrameView{}
	}
	//: the algorithm-id byte must be one of the two frozen ids.
	algo, known := fuzzAlgorithmForID(box[2])
	//: an unknown id is an unsupported / corrupt frame.
	if !known {
		//: reject.
		return fuzzFrameView{}
	}
	//: the inner-Format length is a 2-byte BIG-ENDIAN field — both bytes count.
	nameLen := int(binary.BigEndian.Uint16(box[fuzzFormatLenStart:fuzzFrameHeaderLen]))
	//: the Format bytes (and a possibly-empty payload) must fit the buffer.
	if fuzzFrameHeaderLen+nameLen > len(box) {
		//: the length field overruns the buffer — corruption.
		return fuzzFrameView{}
	}
	//: slice the inner Format, then the remaining compressed payload.
	return fuzzFrameView{
		algo:    algo,
		format:  codec.Format(box[fuzzFrameHeaderLen : fuzzFrameHeaderLen+nameLen]),
		payload: box[fuzzFrameHeaderLen+nameLen:],
		ok:      true,
	}
}

// fuzzAlgorithmForID maps a frozen frame id back to its CompressAlgorithm.
func fuzzAlgorithmForID(id byte) (algo codec.CompressAlgorithm, ok bool) {
	//: 0x01 is the gzip frame id.
	if id == fuzzAlgGzip {
		//: map it back to the gzip algorithm.
		return codec.Gzip, true
	}
	//: 0x02 is the flate frame id.
	if id == fuzzAlgFlate {
		//: map it back to the flate algorithm.
		return codec.Flate, true
	}
	//: any other id is unsupported in this frame version.
	return "", false
}

// fuzzRefDecompress decompresses payload with the stdlib scheme the frame names,
// bounded at the frame-layer ceiling so a bomb cannot OOM the fuzz worker. The
// tri-state return separates "corrupt stream" from "legitimately huge output".
func fuzzRefDecompress(algo codec.CompressAlgorithm, payload []byte) (plain []byte, status int) {
	//: open the stdlib reader matching the frame's algorithm id.
	r, ok := fuzzRefReader(algo, payload)
	//: a gzip header that does not parse is a corrupt stream.
	if !ok {
		//: nothing to drain.
		return nil, fuzzDecompressFailed
	}
	//: read one byte past the ceiling so an over-cap stream is detectable.
	out, rErr := io.ReadAll(io.LimitReader(r, int64(fuzzMaxDecompressedFrameBytes)+1))
	//: Close is where flate/gzip surface a truncated or corrupt tail.
	cErr := r.Close()
	//: either fault means the payload is not a clean stream.
	if rErr != nil || cErr != nil {
		//: corrupt or truncated.
		return nil, fuzzDecompressFailed
	}
	//: more than the ceiling means the bomb guard would reject it regardless.
	if len(out) > fuzzMaxDecompressedFrameBytes {
		//: over the frame ceiling.
		return nil, fuzzDecompressOverCap
	}
	//: a clean, in-bounds decompression.
	return out, fuzzDecompressOK
}

// fuzzRefReader builds the stdlib reader for a frame algorithm.
func fuzzRefReader(algo codec.CompressAlgorithm, payload []byte) (stream io.ReadCloser, ok bool) {
	//: gzip validates its header eagerly, exactly as the SDK scheme does.
	if algo == codec.Gzip {
		//: a bad gzip header fails here.
		gr, err := gzip.NewReader(bytes.NewReader(payload))
		//: surface the header fault as a corrupt stream.
		if err != nil {
			//: no reader to hand back.
			return nil, false
		}
		//: header accepted.
		return gr, true
	}
	//: raw DEFLATE has no header; faults surface on read/close.
	if algo == codec.Flate {
		//: flate.NewReader never fails eagerly.
		return flate.NewReader(bytes.NewReader(payload)), true
	}
	//: no other algorithm has a frame id.
	return nil, false
}

// fuzzRefIsBomb reimplements isBomb from the documented thresholds.
func fuzzRefIsBomb(inLen, outLen int) bool {
	//: the absolute ceiling is an unconditional rejection.
	if outLen > fuzzMaxDecompressedFrameBytes {
		//: over the hard ceiling — a bomb.
		return true
	}
	//: below the small-payload floor the ratio guard does not apply.
	if outLen <= fuzzBombFloorBytes {
		//: tiny outputs are always allowed.
		return false
	}
	//: above the floor, bound the expansion ratio against the compressed size.
	return outLen > inLen*fuzzMaxExpansionRatio
}

// FuzzUnmarshalCompressed drives both legs of the compressed-frame contract.
//
// Invariants asserted (beyond "it does not panic", which the runtime enforces):
//
//  1. NON-ORACLE REJECTION — every box the reference header parser rejects must
//     come back as EXACTLY CompressedFrameInvalid, carrying none of the
//     sub-check reasons in fuzzLeakReasons. Verified against compressed.go:
//     UnmarshalCompressed returns the bare coretransform.CompressedFrameInvalid
//     sentinel when parseFrame reports ok=false.
//  2. CONVERSE — on a box the reference ACCEPTS, CompressedFrameInvalid may only
//     come from the bomb guard, so the assertion recomputes the decompression
//     and the guard rather than trusting the verdict.
//  3. FRAME ROUND-TRIP — for a fuzzed value and a fuzzed Format/algorithm,
//     MarshalCompressed must emit a frame whose header the reference parses back
//     to the same algorithm and Format, and whose payload decompresses BYTE-EXACT
//     to codec.Marshal's output.
//  4. FACADE EQUIVALENCE — UnmarshalCompressed ∘ MarshalCompressed must agree
//     with Unmarshal ∘ Marshal on both the value and the error reason, for EVERY
//     Format. This is the total form of "round-trip returns v": it holds where a
//     literal got == v does not (encoding/json replaces invalid UTF-8 with
//     U+FFFD, measured), so the literal form is asserted for JSON over valid
//     UTF-8, where it is exact.
func FuzzUnmarshalCompressed(f *testing.F) {
	//: seed both legs from one corpus shape.
	fuzzSeedCompressed(f)
	//: box drives the attacker path; the other three drive the success path.
	f.Fuzz(func(t *testing.T, box []byte, algoSel byte, formatSel byte, value string) {
		//: leg 1 — arbitrary bytes must be rejected without an oracle.
		fuzzAssertFrameRejection(t, box)
		//: leg 2 — a real value must survive the frame unchanged.
		fuzzAssertCompressedRoundTrip(t, algoSel, formatSel, value)
	})
}

// fuzzSeedCompressed adds the structural corpus: two REAL frames (so the
// mutator starts from something that parses) plus every degenerate header shape.
func fuzzSeedCompressed(f *testing.F) {
	//: a real gzip frame — the only seed a byte mutator can degrade usefully.
	goodGzip := fuzzSeedFrame(f, codec.Gzip)
	//: a real flate frame exercises the second algorithm id.
	goodFlate := fuzzSeedFrame(f, codec.Flate)
	//: a frame whose gzip body is valid but whose inner Format is unregistered.
	unknownFormat := fuzzBuildFrame("no-such-format", fuzzGzipBytes(f, []byte("{}")))
	//: a frame with an EMPTY inner Format (nameLen == 0, payload present).
	emptyFormat := fuzzBuildFrame("", fuzzGzipBytes(f, []byte("{}")))
	//: nameLen pointing exactly at the end of the buffer — zero payload left.
	exactEnd := []byte{fuzzFrameMagic, fuzzFrameVersion, fuzzAlgGzip, 0x00, 0x04, 'j', 's', 'o', 'n'}
	//: every structural degenerate, each paired with a benign value leg.
	boxes := [][]byte{
		goodGzip,
		goodFlate,
		nil,
		{},
		append([]byte{0x00}, goodGzip[1:]...),
		append([]byte{fuzzFrameMagic, 0x02}, goodGzip[2:]...),
		append([]byte{fuzzFrameMagic, fuzzFrameVersion, 0x00}, goodGzip[3:]...),
		append([]byte{fuzzFrameMagic, fuzzFrameVersion, 0x03}, goodGzip[3:]...),
		append([]byte{fuzzFrameMagic, fuzzFrameVersion, 0xFF}, goodGzip[3:]...),
		{fuzzFrameMagic, fuzzFrameVersion, fuzzAlgGzip, 0xFF, 0xFF},
		{fuzzFrameMagic, fuzzFrameVersion, fuzzAlgGzip, 0x00, 0x00},
		exactEnd,
		unknownFormat,
		emptyFormat,
		goodGzip[:len(goodGzip)-3],
		fuzzCorrupt(goodGzip),
	}
	//: the box leg takes each degenerate; the value leg stays neutral.
	for _, b := range boxes {
		//: one corpus entry per structural shape.
		f.Add(b, byte(0), byte(0), "")
	}
	//: value-leg seeds: both algorithms, several Format name lengths, and
	//: strings the JSON path treats specially (empty, unicode, invalid UTF-8).
	f.Add([]byte(nil), byte(0), byte(0), "kitsune")
	f.Add([]byte(nil), byte(1), byte(0), "héllo")
	f.Add([]byte(nil), byte(0), byte(12), "x")
	f.Add([]byte(nil), byte(1), byte(23), "")
	f.Add([]byte(nil), byte(0), byte(0), "\xff\xfe")
}

// fuzzSeedFrame builds a real frame with MarshalCompressed so the corpus starts
// from bytes the production writer actually emits.
func fuzzSeedFrame(f *testing.F, algo codec.CompressAlgorithm) []byte {
	//: encode a small JSON value through the real writer.
	box, err := codec.MarshalCompressed(codec.JSON, algo, map[string]any{"name": "kitsune", "n": 42})
	//: a broken fixture would silently empty the corpus.
	if err != nil {
		//: stop before the corpus is built on sand.
		f.Fatalf("seed MarshalCompressed(json,%s): %v", algo, err)
	}
	//: the real frame bytes.
	return box
}

// fuzzGzipBytes gzip-compresses raw with the stdlib, matching the body shape the
// gzip scheme writes, so a hand-built frame decompresses cleanly.
func fuzzGzipBytes(f *testing.F, raw []byte) []byte {
	//: collect the compressed bytes in memory.
	var buf bytes.Buffer
	//: standard gzip writer matches the SDK's gzip transform body.
	w := gzip.NewWriter(&buf)
	//: a write fault here is a harness fault, not a contract violation.
	if _, err := w.Write(raw); err != nil {
		//: stop the seed build.
		f.Fatalf("seed gzip write: %v", err)
	}
	//: Close flushes the trailer — required for a valid stream.
	if err := w.Close(); err != nil {
		//: stop the seed build.
		f.Fatalf("seed gzip close: %v", err)
	}
	//: a complete gzip stream.
	return buf.Bytes()
}

// fuzzBuildFrame assembles the frozen layout around a caller-supplied body so a
// seed can carry an inner Format MarshalCompressed would never write.
func fuzzBuildFrame(format string, body []byte) []byte {
	//: magic + frame version + gzip algorithm id.
	frame := []byte{fuzzFrameMagic, fuzzFrameVersion, fuzzAlgGzip}
	//: 2-byte big-endian inner-Format length field.
	frame = binary.BigEndian.AppendUint16(frame, uint16(len(format)))
	//: inner Format string travels next so the reader needs no Format arg.
	frame = append(frame, []byte(format)...)
	//: compressed body closes the frame.
	return append(frame, body...)
}

// fuzzCorrupt returns a copy of box with its last payload byte flipped, giving a
// frame whose header is valid and whose compressed body fails its checksum.
func fuzzCorrupt(box []byte) []byte {
	//: copy so the caller's seed frame stays intact.
	out := bytes.Clone(box)
	//: flip the final byte — inside the gzip CRC/ISIZE trailer.
	if len(out) > 0 {
		//: a single bit is enough to fail the checksum.
		out[len(out)-1] ^= 0xFF
	}
	//: a structurally valid frame with a corrupt body.
	return out
}

// fuzzAssertFrameRejection drives leg 1: arbitrary bytes through
// UnmarshalCompressed, checked against the reference header parser.
func fuzzAssertFrameRejection(t *testing.T, box []byte) {
	//: reference verdict, computed from the wire contract alone.
	view := fuzzParseFrame(box)
	//: the decode target is irrelevant — the frame is judged before decoding.
	var sink map[string]any
	//: the production verdict.
	err := codec.UnmarshalCompressed(box, &sink)
	//: a header the reference rejects must come back non-oracle.
	if !view.ok {
		//: exactly one reason is contractually allowed here.
		if !errs.HasReason(err, fuzzFrameInvalidReason) {
			//: nil, or any other error, breaks the documented contract.
			t.Fatalf("UnmarshalCompressed(%x): want %s, got %v", box, fuzzFrameInvalidReason, err)
		}
		//: and it must not name which sub-check failed.
		fuzzAssertNoOracleLeak(t, box, err)
		//: leg 1 done for a rejected header.
		return
	}
	//: on an accepted header, only the bomb guard may raise frame-invalid.
	if !errs.HasReason(err, fuzzFrameInvalidReason) {
		//: any other outcome is the codec/compressor layer, not the frame.
		return
	}
	//: prove the bomb guard is what fired, rather than assuming it.
	fuzzAssertBombGuard(t, box, view)
}

// fuzzAssertNoOracleLeak fails when a frame-invalid rejection also carries a
// reason that names the sub-check that failed.
func fuzzAssertNoOracleLeak(t *testing.T, box []byte, err error) {
	//: walk every reason the frame layer must keep to itself.
	for _, leak := range fuzzLeakReasons {
		//: a leaked reason turns the rejection into an oracle.
		if errs.HasReason(err, leak) {
			//: name the leak and the input that produced it.
			t.Fatalf("UnmarshalCompressed(%x): rejection leaked %s (%v)", box, leak, err)
		}
	}
}

// fuzzAssertBombGuard fails unless a frame-invalid verdict on a structurally
// valid header is explained by the decompression-bomb guard.
func fuzzAssertBombGuard(t *testing.T, box []byte, view fuzzFrameView) {
	//: recompute the decompression the frame layer performed.
	plain, status := fuzzRefDecompress(view.algo, view.payload)
	//: output past the frame ceiling IS a bomb, whatever its exact size.
	if status == fuzzDecompressOverCap {
		//: consistent with the guard — nothing to prove.
		return
	}
	//: a stream that does not decompress must surface the scheme's fault.
	if status != fuzzDecompressOK {
		//: frame-invalid here would mask a compressor fault.
		t.Fatalf("UnmarshalCompressed(%x): %s on a header-valid frame whose payload does not decompress", box, fuzzFrameInvalidReason)
	}
	//: a clean, in-bounds decompression must not be called a bomb.
	if !fuzzRefIsBomb(len(view.payload), len(plain)) {
		//: over-rejection — a valid frame was refused.
		t.Fatalf("UnmarshalCompressed(%x): %s on a header-valid, non-bomb frame (%d → %d bytes)", box, fuzzFrameInvalidReason, len(view.payload), len(plain))
	}
}

// fuzzAssertCompressedRoundTrip drives leg 2: a fuzzed value through
// MarshalCompressed and back, with the frame bytes checked independently.
func fuzzAssertCompressedRoundTrip(t *testing.T, algoSel, formatSel byte, value string) {
	//: one fuzz byte selects the algorithm, another the inner Format.
	algo := codec.Gzip
	//: odd selector picks the second frozen scheme.
	if algoSel%2 == 1 {
		//: raw DEFLATE.
		algo = codec.Flate
	}
	//: modulo keeps the selector inside the registered Format list.
	format := fuzzFormats[int(formatSel)%len(fuzzFormats)]
	//: the uncompressed reference encoding for the same (Format, value).
	enc, mErr := codec.Marshal(format, value)
	//: the framed encoding under test.
	box, cErr := codec.MarshalCompressed(format, algo, value)
	//: MarshalCompressed forwards the codec error untouched — same nil-ness.
	if (mErr == nil) != (cErr == nil) {
		//: a divergence means the frame layer invented or swallowed a fault.
		t.Fatalf("Marshal(%s)=%v but MarshalCompressed(%s,%s)=%v", format, mErr, format, algo, cErr)
	}
	//: a Format that cannot encode this value ends leg 2 here.
	if mErr != nil {
		//: nothing to round-trip.
		return
	}
	//: the emitted frame must parse back to exactly what was asked for.
	fuzzAssertFrameShape(t, box, enc, algo, format)
	//: and decoding it must agree with the uncompressed facade path.
	fuzzAssertFacadeEquivalence(t, box, enc, format, value)
}

// fuzzAssertFrameShape checks the bytes MarshalCompressed emitted against the
// reference header parser and the reference decompressor.
func fuzzAssertFrameShape(t *testing.T, box, enc []byte, algo codec.CompressAlgorithm, format codec.Format) {
	//: reference verdict on the production writer's own output.
	view := fuzzParseFrame(box)
	//: a frame this writer emitted must always parse.
	if !view.ok {
		//: the writer and the reader disagree on the layout.
		t.Fatalf("MarshalCompressed(%s,%s): emitted a frame the reference rejects (%x)", format, algo, box)
	}
	//: the header must name the algorithm that was requested.
	if view.algo != algo {
		//: an algorithm-id mismatch silently changes the decompressor.
		t.Fatalf("MarshalCompressed(%s,%s): frame names algorithm %q", format, algo, view.algo)
	}
	//: the header must carry the inner Format verbatim, length field included.
	if view.format != format {
		//: a Format mismatch means the 2-byte length field is wrong.
		t.Fatalf("MarshalCompressed(%s,%s): frame names Format %q", format, algo, view.format)
	}
	//: multipart re-emits a RANDOM RFC 2046 boundary in the body (measured:
	//: two Marshal calls on the same value differ), so its payload cannot be
	//: compared byte-exact against a second encoding. Every other Format is.
	if format == codec.Multipart {
		//: byte-exactness is not a property multipart has.
		return
	}
	//: the body must decompress back to exactly codec.Marshal's output.
	plain, status := fuzzRefDecompress(algo, view.payload)
	//: the writer's own body must be a clean stream.
	if status != fuzzDecompressOK {
		//: a body the stdlib cannot read was mis-written.
		t.Fatalf("MarshalCompressed(%s,%s): payload does not decompress (status %d)", format, algo, status)
	}
	//: byte-exact, not merely decodable.
	if !bytes.Equal(plain, enc) {
		//: the frame altered the codec's bytes in transit.
		t.Fatalf("MarshalCompressed(%s,%s): payload decompressed to %d bytes, want the %d bytes Marshal produced", format, algo, len(plain), len(enc))
	}
}

// fuzzAssertFacadeEquivalence checks that decoding through the frame agrees with
// decoding the same bytes through the plain facade — value and error reason.
func fuzzAssertFacadeEquivalence(t *testing.T, box, enc []byte, format codec.Format, value string) {
	//: the framed decode under test.
	var got string
	//: the uncompressed reference decode.
	var want string
	//: run both legs of the comparison.
	uErr := codec.UnmarshalCompressed(box, &got)
	//: the facade path the frame claims to wrap.
	dErr := codec.Unmarshal(format, enc, &want)
	//: the frame must not turn a success into a failure, or the reverse.
	if (uErr == nil) != (dErr == nil) {
		//: a divergence means the frame layer is not transparent.
		t.Fatalf("UnmarshalCompressed(%s)=%v but Unmarshal(%s)=%v", format, uErr, format, dErr)
	}
	//: on a shared failure the forwarded reason must be the same one.
	if uErr != nil {
		//: compare the two origins.
		uR, _ := errs.ReasonOf(uErr)
		//: the plain-facade origin.
		dR, _ := errs.ReasonOf(dErr)
		//: a different reason means the frame layer re-labelled the fault.
		if uR != dR {
			//: origin must win on both paths.
			t.Fatalf("UnmarshalCompressed(%s) reason %q but Unmarshal reason %q", format, uR, dR)
		}
		//: nothing further to compare.
		return
	}
	//: both succeeded — the decoded values must be identical.
	if got != want {
		//: the frame changed the decoded value.
		t.Fatalf("UnmarshalCompressed(%s)=%q but Unmarshal(%s)=%q", format, got, format, want)
	}
	//: for JSON over valid UTF-8 the round trip is exact, so assert the literal
	//: "MarshalCompressed then UnmarshalCompressed returns v". Invalid UTF-8 is
	//: excluded because encoding/json replaces it with U+FFFD (measured), which
	//: is a json contract, not a frame-layer defect.
	if format == codec.JSON && utf8.ValidString(value) && got != value {
		//: the success path lost the value.
		t.Fatalf("round-trip(json): got %q want %q", got, value)
	}
}
