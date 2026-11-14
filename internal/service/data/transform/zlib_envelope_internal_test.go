package transform

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// stdlibZlibDecode is the reference every row below is judged against: the
// standard library's compress/zlib over the whole input, read to its end. It is
// what zlibDecompressCore was before kitsunium/sdk#1102 took the RFC 1950
// envelope into this package, so "same bytes, same refusal" is checked against
// the code it replaced and not against a restatement of it.
func stdlibZlibDecode(src []byte) (plain []byte, err error) {
	zr, nerr := zlib.NewReader(bytes.NewReader(src))
	//: a refused header is the stdlib's whole verdict.
	if nerr != nil {
		//: no decoder to read.
		return nil, nerr
	}
	plain, err = io.ReadAll(zr)
	//: Close is folded in as the core folds it.
	if cerr := zr.Close(); cerr != nil && err == nil {
		err = cerr
	}
	//: the plaintext and the verdict.
	return plain, err
}

// zlibEnvelopeFrame builds a valid RFC 1950 frame of plain at the default level.
func zlibEnvelopeFrame(tb testing.TB, plain []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	//: a buffer-backed writer cannot fail; a failure is a broken fixture.
	if _, err := zw.Write(plain); err != nil {
		tb.Fatalf("fixture Write: %v", err)
	}
	if err := zw.Close(); err != nil {
		tb.Fatalf("fixture Close: %v", err)
	}
	return buf.Bytes()
}

// zlibHeaderWith returns the two header bytes for cmf whose FLG carries the
// FDICT bit as asked and a check field that makes the pair a multiple of 31.
func zlibHeaderWith(cmf byte, dict bool) []byte {
	flg := byte(0)
	//: the FDICT bit is FLG's bit 5.
	if dict {
		flg = zlibFlagDict
	}
	//: the check bits are the low five of FLG, chosen so the pair divides by 31.
	flg += byte(31 - (uint16(cmf)<<8|uint16(flg))%31)
	return []byte{cmf, flg}
}

// deflateBody is the raw DEFLATE stream of plain, the middle of a zlib frame.
func deflateBody(tb testing.TB, plain []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		tb.Fatalf("fixture flate.NewWriter: %v", err)
	}
	if _, werr := fw.Write(plain); werr != nil {
		tb.Fatalf("fixture Write: %v", werr)
	}
	if cerr := fw.Close(); cerr != nil {
		tb.Fatalf("fixture Close: %v", cerr)
	}
	return buf.Bytes()
}

// TestZlibEnvelopeAgreesWithTheStandardLibrary is the guard on reading RFC 1950
// in this package: over valid frames and over every malformed shape the
// envelope has, zlibDecompress yields the bytes compress/zlib yields and a
// refusal the stdlib's own error is found in, by errors.Is. A frame the stdlib
// accepts and this package refuses, or the reverse, fails a row here.
func TestZlibEnvelopeAgreesWithTheStandardLibrary(t *testing.T) {
	t.Parallel()
	text := bytes.Repeat([]byte("kitsunium-sdk zlib envelope "), 5000)
	good := zlibEnvelopeFrame(t, text)
	empty := zlibEnvelopeFrame(t, nil)
	body := deflateBody(t, text)
	sum := make([]byte, zlibTrailerLen)
	binary.BigEndian.PutUint32(sum, 0)
	//: a frame whose trailer is flipped in its last byte.
	badSum := bytes.Clone(good)
	badSum[len(badSum)-1] ^= 0xff
	//: an FDICT frame naming the empty dictionary, DICTID 1, then a valid body
	//: and the sum of the plaintext: the one dictionary compress/zlib accepts.
	dictFrame := append(zlibHeaderWith(0x78, true), 0, 0, 0, 1)
	dictFrame = append(dictFrame, body...)
	dictFrame = append(dictFrame, good[len(good)-zlibTrailerLen:]...)
	type tc struct {
		name string
		src  []byte
	}
	tests := []tc{
		{"a text frame", good},
		{"an empty payload", empty},
		{"bytes after the trailer are ignored", append(bytes.Clone(good), 1, 2, 3)},
		{"an FDICT header naming the empty dictionary", dictFrame},
		{"an FDICT header naming another dictionary", append(zlibHeaderWith(0x78, true), 0xde, 0xad, 0xbe, 0xef, 0)},
		{"an FDICT header cut short", append(zlibHeaderWith(0x78, true), 0, 0)},
		{"no bytes", nil},
		{"one byte", []byte{0x78}},
		{"a method that is not DEFLATE", append(zlibHeaderWith(0x79, false), body...)},
		{"a window past 32 KiB", append(zlibHeaderWith(0x88, false), body...)},
		{"check bits that do not divide by 31", append([]byte{0x78, 0x9d}, body...)},
		{"a wrong checksum", badSum},
		{"a trailer cut to three bytes", good[:len(good)-1]},
		{"a trailer cut to one byte", good[:len(good)-3]},
		{"no trailer at all", good[:len(good)-zlibTrailerLen]},
		{"a body cut in half", good[:len(good)/2]},
		{"header only", good[:zlibHeaderLen]},
		{"a corrupt body", append(zlibHeaderWith(0x78, false), 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		want, werr := stdlibZlibDecode(c.src)
		got, gerr := zlibDecompress(nil, c.src, maxDecompressedBytes)
		//: acceptance agrees, and with it the bytes.
		if (werr == nil) != (gerr == nil) {
			t.Fatalf("%s: stdlib err=%v, package err=%v", c.name, werr, gerr)
		}
		//: a frame both accept decodes to the same plaintext.
		if werr == nil {
			if !bytes.Equal(got, want) {
				t.Errorf("%s: %d bytes, the stdlib's are %d", c.name, len(got), len(want))
			}
			return
		}
		//: a frame both refuse is refused for the stdlib's reason, under the
		//: scheme's sentinel.
		if !errors.Is(gerr, werr) {
			t.Errorf("%s: err=%v does not wrap the stdlib's %v", c.name, gerr, werr)
		}
		if !errs.HasCode(gerr, coretransform.CodeZlibFailed) {
			t.Errorf("%s: err=%v is not ZlibFailed", c.name, gerr)
		}
		//: a refusal leaves the caller's dst as it came.
		prefix := []byte("keep")
		kept, perr := zlibDecompress(prefix, c.src, maxDecompressedBytes)
		if perr == nil || !bytes.Equal(kept, prefix) {
			t.Errorf("%s: dst after a refusal = %q, %v", c.name, kept, perr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestZlibEnvelopeHoldsTheCeilingAtItsEdge pins the three edges of the bound
// through the package's own envelope: a stream of exactly max bytes decodes, one
// of max+1 is over the ceiling, and appending onto a prefix keeps the prefix.
// The trailer is not read past the ceiling, so an over-cap frame with a wrong
// checksum is over the ceiling and not a checksum fault, as with the stdlib.
func TestZlibEnvelopeHoldsTheCeilingAtItsEdge(t *testing.T) {
	t.Parallel()
	text := bytes.Repeat([]byte("kitsunium-sdk "), 1000)
	good := zlibEnvelopeFrame(t, text)
	badSum := bytes.Clone(good)
	badSum[len(badSum)-1] ^= 0xff
	type tc struct {
		name     string
		src      []byte
		max      int64
		prefix   string
		wantOver bool
		wantErr  bool
	}
	tests := []tc{
		{"exactly max bytes", good, int64(len(text)), "", false, false},
		{"max plus one is over", good, int64(len(text)) - 1, "", true, false},
		{"a ceiling of zero admits an empty stream", zlibEnvelopeFrame(t, nil), 0, "", false, false},
		{"a ceiling of zero refuses any other", good, 0, "", true, false},
		{"the prefix is kept", good, int64(len(text)), "kept:", false, false},
		{"an over-cap frame is over before its checksum is read", badSum, int64(len(text)) - 1, "", true, false},
		{"a bad checksum within the ceiling is a fault", badSum, int64(len(text)), "", false, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		out, over, err := zlibDecompressCore([]byte(c.prefix), c.src, c.max)
		//: the fault, or its absence.
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: err=%v want error %v", c.name, err, c.wantErr)
		}
		//: the ceiling's verdict.
		if over != c.wantOver {
			t.Fatalf("%s: over=%v want %v", c.name, over, c.wantOver)
		}
		//: on success the prefix and then the plaintext; otherwise the prefix.
		if err == nil && !over && !bytes.Equal(out, append([]byte(c.prefix), zlibPlain(t, c.src)...)) {
			t.Errorf("%s: out is not prefix + plaintext", c.name)
		}
		if (err != nil || over) && string(out) != c.prefix {
			t.Errorf("%s: dst after a refusal = %q want %q", c.name, out, c.prefix)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// zlibPlain decodes a valid frame with the standard library, for a row's want.
func zlibPlain(tb testing.TB, src []byte) []byte {
	tb.Helper()
	plain, err := stdlibZlibDecode(src)
	//: a fixture that does not decode is a broken test.
	if err != nil && !errors.Is(err, zlib.ErrChecksum) {
		tb.Fatalf("fixture decode: %v", err)
	}
	return plain
}
