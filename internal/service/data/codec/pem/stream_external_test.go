package pem_test

import (
	"bytes"
	stdpem "encoding/pem"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	corecodec "github.com/kitsunium/sdk/internal/core/data/codec"
	corepem "github.com/kitsunium/sdk/internal/core/data/codec/pem"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/pem"
)

// streaming is the codec under test as a StreamingCodec.
func streaming(t *testing.T) corecodec.StreamingCodec {
	t.Helper()
	sc, ok := pem.New().(corecodec.StreamingCodec)
	if !ok {
		t.Fatal("pem does not implement codec.StreamingCodec")
	}
	return sc
}

// chain is three blocks with text before, between and after them, as a
// certificate chain file often carries.
func chain(t *testing.T) ([]byte, []*stdpem.Block) {
	t.Helper()
	blocks := []*stdpem.Block{
		{Type: "CERTIFICATE", Bytes: bytes.Repeat([]byte{1}, 700)},
		{Type: "RSA PRIVATE KEY", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: []byte("key")},
		{Type: "CERTIFICATE", Bytes: bytes.Repeat([]byte{3}, 5000)},
	}
	var buf bytes.Buffer
	buf.WriteString("subject=CN=leaf\n")
	for i, b := range blocks {
		if i == 1 {
			buf.WriteString("\nissuer=CN=intermediate\n")
		}
		if err := stdpem.Encode(&buf, b); err != nil {
			t.Fatal(err)
		}
	}
	buf.WriteString("trailing text\n")
	return buf.Bytes(), blocks
}

// TestStreamDecodesOneBlockPerDecode is #901 for PEM: a chain decodes one
// *pem.Block per Decode, the blocks encoding/pem.Decode finds in a loop,
// More true per block then false, and io.EOF once drained — whole, and one
// byte at a time.
func TestStreamDecodesOneBlockPerDecode(t *testing.T) {
	t.Parallel()
	data, want := chain(t)
	for name, r := range map[string]func() io.Reader{
		"whole":    func() io.Reader { return bytes.NewReader(data) },
		"one byte": func() io.Reader { return iotest.OneByteReader(bytes.NewReader(data)) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dec := streaming(t).NewDecoder(r())
			var got []*stdpem.Block
			for dec.More() {
				var b *stdpem.Block
				if err := dec.Decode(&b); err != nil {
					t.Fatalf("Decode: %v", err)
				}
				got = append(got, b)
			}
			if len(got) != len(want) {
				t.Fatalf("decoded %d blocks, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i].Type != want[i].Type || !bytes.Equal(got[i].Bytes, want[i].Bytes) || len(got[i].Headers) != len(want[i].Headers) {
					t.Fatalf("block %d = %+v", i, got[i])
				}
			}
			var b *stdpem.Block
			if err := dec.Decode(&b); err != io.EOF {
				t.Fatalf("Decode after the last block = %v, want io.EOF", err)
			}
		})
	}
}

// TestStreamRefusesWhatUnmarshalRefuses pins the decoder's refusals: a block
// cut short at the end of the stream is UNMARSHAL_FAILED, a wrong target
// VALUE_INVALID, and a block past the 10 MiB ceiling SIZE_EXCEEDED without
// the stream being read past it.
func TestStreamRefusesWhatUnmarshalRefuses(t *testing.T) {
	t.Parallel()
	whole := stdpem.EncodeToMemory(&stdpem.Block{Type: "A", Bytes: []byte("abc")})
	cut := streaming(t).NewDecoder(bytes.NewReader(whole[:len(whole)-8]))
	var b *stdpem.Block
	if err := cut.Decode(&b); !errs.HasCode(err, corepem.CodePEMUnmarshalFailed) {
		t.Fatalf("truncated block: %v, want UNMARSHAL_FAILED", err)
	}
	if err := streaming(t).NewDecoder(bytes.NewReader(whole)).Decode(new(int)); !errs.HasCode(err, corepem.CodePEMValueInvalid) {
		t.Fatalf("wrong target: %v, want VALUE_INVALID", err)
	}
	if err := streaming(t).NewDecoder(strings.NewReader("no block here\n")).Decode(&b); err != io.EOF {
		t.Fatalf("a stream without a block: %v, want io.EOF", err)
	}
	const ceiling = 10 << 20
	huge := io.MultiReader(strings.NewReader("-----BEGIN HUGE-----\n"), &repeatReader{b: 'A', n: ceiling + 1}, strings.NewReader("\n-----END HUGE-----\n"))
	src := &countingReader{r: huge}
	err := streaming(t).NewDecoder(src).Decode(&b)
	if !errs.HasCode(err, corepem.CodePEMSizeExceeded) || !errs.HasReason(err, "SIZE_EXCEEDED") {
		t.Fatalf("oversized block: %v, want SIZE_EXCEEDED", err)
	}
	if src.n > ceiling+64<<10 {
		t.Fatalf("read %d bytes past a %d-byte ceiling", src.n, ceiling)
	}
}

// repeatReader yields n copies of b.
type repeatReader struct {
	b byte
	n int
}

// Read fills p with b until n bytes have been read.
func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	k := min(len(p), r.n)
	for i := range k {
		p[i] = r.b
	}
	r.n -= k
	return k, nil
}

// countingReader counts the bytes its reader handed out.
type countingReader struct {
	r io.Reader
	n int
}

// Read reads from the wrapped reader and counts.
func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// TestStreamEncoderWritesMarshalsBlocks pins the encoder: one Encode per block
// writes Marshal's bytes, a refused block writes nothing, and a failing
// writer surfaces as MARSHAL_FAILED.
func TestStreamEncoderWritesMarshalsBlocks(t *testing.T) {
	t.Parallel()
	_, blocks := chain(t)
	var want, buf bytes.Buffer
	enc := streaming(t).NewEncoder(&buf)
	for _, b := range blocks {
		m, err := pem.New().Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		want.Write(m)
		if err := enc.Encode(b); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	if err := enc.Encode(&stdpem.Block{Type: "X", Headers: map[string]string{"a:b": ""}}); !errs.HasCode(err, corepem.CodePEMMarshalFailed) {
		t.Fatalf("a header key with a colon: %v, want MARSHAL_FAILED", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want.Bytes()) {
		t.Fatalf("stream wrote %q, Marshal writes %q", buf.Bytes(), want.Bytes())
	}
	failing := streaming(t).NewEncoder(errWriter{})
	if err := failing.Encode(blocks[0]); !errs.HasCode(err, corepem.CodePEMMarshalFailed) {
		t.Fatalf("a failing writer: %v, want MARSHAL_FAILED", err)
	}
}

// errWriter fails every write.
type errWriter struct{}

// Write fails.
func (errWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
