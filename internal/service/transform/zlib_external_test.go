package transform_test

import (
	"bytes"
	"compress/flate"
	"testing"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svctransform "github.com/kitsunium/sdk/internal/service/transform"
)

func TestZlibRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		payload    []byte
		wantShrink bool
	}
	tests := []tc{
		{"empty", []byte{}, false},
		{"small", []byte("hello, transform"), false},
		{"repetitive shrinks", bytes.Repeat([]byte("kitsunium-sdk "), 4096), true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: zlib round-trip is bidirectional: Compress shrinks, Decompress restores.
		roundTrip(t, "zlib", c.payload, c.wantShrink)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestZlibDecompressGarbage(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		input      []byte
		wantReason string
	}
	tests := []tc{
		{"non-zlib bytes fail", []byte("definitely not a zlib frame"), "ZLIB_FAILED"},
		//: a truncated envelope has a valid 2-byte header, so it survives
		//: zlib.NewReader and only fails while the body is drained.
		{"truncated envelope fails", []byte{0x78, 0x9c, 0x01}, "ZLIB_FAILED"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := resolve(t, "zlib").Decompress(nil, c.input)
		//: garbage input must surface the zlib failure reason.
		if !errs.HasReason(err, c.wantReason) {
			t.Errorf("%s: expected %s, got %v", c.name, c.wantReason, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestZlibRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		algo coretransform.Algorithm
		want coretransform.Algorithm
	}
	tests := []tc{{"zlib resolves to canonical name", "zlib", "zlib"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the resolved scheme must report its canonical Algorithm.
		if got := resolve(t, c.algo).Algorithm(); got != c.want {
			t.Errorf("%s: Algorithm()=%q want %q", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestZlibIsNotFlate pins the distinction the two schemes exist to keep: `zlib`
// is the RFC 1950 envelope (2-byte header + DEFLATE + Adler-32) that HTTP's
// `Content-Encoding: deflate` actually names, while `flate` is the bare RFC 1951
// stream. Neither can read the other's output, and a test that only round-tripped
// each scheme against itself would not notice if one silently became the other.
func TestZlibIsNotFlate(t *testing.T) {
	t.Parallel()
	//: a payload long enough that both schemes emit a real compressed body.
	payload := bytes.Repeat([]byte("kitsunium-sdk "), 4096)
	type tc struct {
		name       string
		encodeWith coretransform.Algorithm
		decodeWith coretransform.Algorithm
		wantReason string
	}
	tests := []tc{
		{"flate bytes are not a zlib envelope", "flate", "zlib", "ZLIB_FAILED"},
		{"zlib bytes are not a raw flate stream", "zlib", "flate", "FLATE_FAILED"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		boxed, cerr := resolve(t, c.encodeWith).Compress(nil, payload)
		if cerr != nil {
			t.Fatalf("%s: %s Compress err=%v", c.name, c.encodeWith, cerr)
		}
		//: cross-decoding must fail typed, never silently yield wrong plaintext.
		got, derr := resolve(t, c.decodeWith).Decompress(nil, boxed)
		if derr == nil {
			t.Fatalf("%s: %s accepted %s bytes (%d decoded)", c.name, c.decodeWith, c.encodeWith, len(got))
		}
		if !errs.HasReason(derr, c.wantReason) {
			t.Errorf("%s: expected %s, got %v", c.name, c.wantReason, derr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestZlibEnvelopeWrapsFlateBody pins the structural relationship the schemes
// document: strip zlib's 2-byte header and 4-byte Adler-32 trailer and what is
// left is exactly a raw DEFLATE stream the `flate` scheme decodes. This is the
// interop fact behind the RFC 9110 `deflate` naming confusion, asserted rather
// than only written down.
func TestZlibEnvelopeWrapsFlateBody(t *testing.T) {
	t.Parallel()
	//: a payload long enough that the body dominates the 6 envelope bytes.
	payload := bytes.Repeat([]byte("kitsunium-sdk "), 4096)
	boxed, cerr := resolve(t, "zlib").Compress(nil, payload)
	if cerr != nil {
		t.Fatalf("zlib Compress err=%v", cerr)
	}
	//: 2-byte CMF/FLG header up front, 4-byte Adler-32 checksum at the end.
	const headerLen int = 2
	const trailerLen int = 4
	if len(boxed) <= headerLen+trailerLen {
		t.Fatalf("zlib frame too short to strip: %d bytes", len(boxed))
	}
	//: the middle is a raw DEFLATE stream; decode it with the stdlib directly so
	//: the assertion does not depend on the flate scheme's own bounded wrapper.
	body := boxed[headerLen : len(boxed)-trailerLen]
	got, derr := readAllFlate(t, body)
	if derr != nil {
		t.Fatalf("stripped zlib body did not decode as raw flate: %v", derr)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("stripped-envelope mismatch (got %d, want %d)", len(got), len(payload))
	}
}

// readAllFlate inflates a raw DEFLATE stream with the stdlib reader, returning
// the plaintext. Test-local so the envelope assertion above stays independent of
// the flate scheme under test.
func readAllFlate(t *testing.T, body []byte) ([]byte, error) {
	t.Helper()
	//: raw DEFLATE has no header, so the reader never fails at construction.
	r := flate.NewReader(bytes.NewReader(body))
	//: drain into a buffer; the input is a test fixture of known size.
	var out bytes.Buffer
	//: ReadFrom surfaces any inflate fault from the stripped body.
	if _, err := out.ReadFrom(r); err != nil {
		//: hand the fault back so the caller reports it in context.
		return nil, err
	}
	//: Close is checked so a truncated stream cannot pass as a clean decode.
	if err := r.Close(); err != nil {
		//: a close fault means the stream was not a complete DEFLATE body.
		return nil, err
	}
	//: the inflated plaintext.
	return out.Bytes(), nil
}

// ensure the blank-import side effect is referenced for zlib as well.
var _ = svctransform.ZlibCompressor
