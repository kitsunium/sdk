// Package baseenc — the buffered decode reader used by variants the stdlib has
// no streaming decoder for. Everything interesting here happens on the FIRST
// Read: the drain, the size cap and the decode all land there, and a caller
// that only ever reads once would never see a later failure.
package baseenc

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// failingReader reports a fixed error rather than data, so the drain's error
// path is exercised without a real broken pipe.
type failingReader struct{ err error }

// Read always fails with the configured error.
func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

// Test_streamDecodeLimit pins which cap each variant streams under. The
// base-conversion variants are O(n²), so they must NOT inherit the 10 MiB block
// cap: NewDecoder would otherwise be a way around the tighter limit Unmarshal
// enforces, which is the whole reason the tighter limit exists.
func Test_streamDecodeLimit(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    variant
		want int
	}
	tests := []tc{
		{"base45 uses the block cap", variantBase45, maxBaseEncBytes},
		{"base64 uses the block cap", variantBase64, maxBaseEncBytes},
		{"base32 uses the block cap", variantBase32, maxBaseEncBytes},
		{"base58 uses the expansion-aware cap", variantBase58, maxConvEncodedBytes},
		{"base62 uses the expansion-aware cap", variantBase62, maxConvEncodedBytes},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := streamDecodeLimit(c.v); got != c.want {
			t.Errorf("streamDecodeLimit(%v) = %d, want %d", c.v, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the quadratic variants must be capped strictly below the block cap, or
	//: streaming would be a way around the tighter limit.
	if maxConvEncodedBytes >= maxBaseEncBytes {
		t.Errorf("the base-conversion stream cap (%d) is not below the block cap (%d)",
			maxConvEncodedBytes, maxBaseEncBytes)
	}
}

// Test_decodeAllReader_Read pins the first-Read semantics: the whole source is
// drained, the decode runs once, and every failure surfaces there rather than
// as a short read the caller would mistake for end-of-stream.
func Test_decodeAllReader_Read(t *testing.T) {
	t.Parallel()
	sourceErr := errors.New("the source went away")

	type tc struct {
		name string
		v    variant
		src  func(t *testing.T) io.Reader
		want string
		//: the typed code the first Read must report, or zero for success.
		wantCode kerrs.Code
		//: set when the failure is the source's own error rather than a typed
		//: one, which must reach the caller verbatim.
		wantSourceErr bool
	}
	tests := []tc{
		{
			name: "a base45 payload decodes",
			v:    variantBase45,
			src: func(t *testing.T) io.Reader {
				t.Helper()
				return bytes.NewReader(encodeBase45([]byte("Hello!!")))
			},
			want: "Hello!!",
		},
		{
			name: "an empty source decodes to nothing",
			v:    variantBase45,
			src:  func(*testing.T) io.Reader { return strings.NewReader("") },
			want: "",
		},
		{
			name:     "a malformed payload fails on the first Read",
			v:        variantBase45,
			src:      func(*testing.T) io.Reader { return strings.NewReader("ab8") },
			wantCode: CodeBaseEncDecodeFailed,
		},
		{
			//: one byte past the cap: the refusal must fire before the decode,
			//: so a hostile stream cannot spend the quadratic work first.
			name: "a source over the cap is refused",
			v:    variantBase58,
			src: func(*testing.T) io.Reader {
				return bytes.NewReader(bytes.Repeat([]byte("1"), maxConvEncodedBytes+1))
			},
			wantCode: CodeBaseEncSizeExceeded,
		},
		{
			name:          "a source that fails reports its own error",
			v:             variantBase45,
			src:           func(*testing.T) io.Reader { return failingReader{err: sourceErr} },
			wantSourceErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &decodeAllReader{src: c.src(t), v: c.v}

		got, err := io.ReadAll(r)

		switch {
		case c.wantSourceErr:
			//: a transport failure is not ours to relabel; the caller needs to
			//: match on the error its own reader produced.
			if !errors.Is(err, sourceErr) {
				t.Fatalf("Read = %v, want the source's own error", err)
			}
		case c.wantCode != 0:
			if !kerrs.HasCode(err, c.wantCode) {
				t.Fatalf("Read = %v, want code %v", err, c.wantCode)
			}
		default:
			if err != nil {
				t.Fatalf("Read = %v, want nil", err)
			}
			if string(got) != c.want {
				t.Errorf("Read yielded %q, want %q", got, c.want)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_decodeAllReader_Read_Incremental pins that the decode happens exactly
// once. io.ReadAll hides this by draining in one go; a caller reading a byte at
// a time would re-run the whole transform per call if `done` were not latched.
func Test_decodeAllReader_Read_Incremental(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		payload string
		chunk   int
	}
	tests := []tc{
		{"one byte at a time", "Hello!!", 1},
		{"in pairs", "Hello!!", 2},
		{"in one oversized read", "Hello!!", 64},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &decodeAllReader{
			src: bytes.NewReader(encodeBase45([]byte(c.payload))),
			v:   variantBase45,
		}

		var out []byte
		buf := make([]byte, c.chunk)
		for {
			n, err := r.Read(buf)
			out = append(out, buf[:n]...)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("Read = %v, want nil or EOF", err)
			}
		}

		if string(out) != c.payload {
			t.Errorf("read back %q, want %q", out, c.payload)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
