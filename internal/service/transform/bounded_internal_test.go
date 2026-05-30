package transform

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Test_readAllBounded white-boxes the shared decompression bound: a within-cap
// reader returns its bytes with overflow=false, while a reader past the cap is
// reported as overflow rather than silently truncated.
func Test_readAllBounded(t *testing.T) {
	t.Parallel()
	type tc struct {
		name         string
		size         int64
		wantOverflow bool
	}
	tests := []tc{
		{"within cap returns bytes", 1024, false},
		{"exactly at cap is allowed", maxDecompressedBytes, false},
		{"one past cap overflows", maxDecompressedBytes + 1, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: small arms read a real string buffer; large arms use a cheap
		//: zero-byte counting reader so the cap is exercised without the alloc.
		var got []byte
		var overflow bool
		var err error
		if c.size <= 4096 {
			got, overflow, err = readAllBounded(strings.NewReader(strings.Repeat("x", int(c.size))))
		} else {
			got, overflow, err = readAllBounded(&zeroReader{remaining: c.size})
		}
		//: the bounded read must never surface a transport error here.
		if err != nil {
			t.Fatalf("%s: unexpected err=%v", c.name, err)
		}
		//: overflow must match the row's expectation.
		if overflow != c.wantOverflow {
			t.Errorf("%s: overflow=%v want %v", c.name, overflow, c.wantOverflow)
		}
		//: a within-cap small read must hand back the exact bytes.
		if !overflow && c.size <= 4096 && !bytes.Equal(got, bytes.Repeat([]byte("x"), len(got))) {
			t.Errorf("%s: returned bytes do not match source", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// zeroReader yields exactly remaining zero bytes then EOF, without allocating
// the payload — lets the over-cap arm exercise the bound cheaply.
type zeroReader struct {
	remaining int64
}

// Read implements io.Reader, emitting zero bytes until remaining is exhausted.
func (z *zeroReader) Read(p []byte) (n int, err error) {
	//: exhausted source signals EOF the way a real reader would.
	if z.remaining <= 0 {
		//: io.EOF is the terminal zero-byte read.
		return 0, io.EOF
	}
	//: hand back as many zero bytes as fit, capped by what remains.
	n = len(p)
	//: never emit more than the source still owes.
	if int64(n) > z.remaining {
		n = int(z.remaining)
	}
	//: account for the bytes consumed this call.
	z.remaining -= int64(n)
	//: the buffer is already zero-valued; report the count.
	return n, nil
}

// Test_zeroReader_Read covers the test helper's Reader so the internal test
// file has no untested function of its own (KTN-TEST-COVERAGE applies here too).
func Test_zeroReader_Read(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		remaining int64
		wantN     int
		wantEOF   bool
	}
	tests := []tc{
		{"emits up to remaining", 3, 3, false},
		{"empty source is EOF", 0, 0, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		z := &zeroReader{remaining: c.remaining}
		buf := make([]byte, 8)
		n, err := z.Read(buf)
		//: the EOF arm must report zero bytes and io.EOF.
		if c.wantEOF {
			if n != 0 || err != io.EOF {
				t.Errorf("%s: n=%d err=%v want 0/EOF", c.name, n, err)
			}
			return
		}
		//: the data arm must report the capped count and no error.
		if n != c.wantN || err != nil {
			t.Errorf("%s: n=%d err=%v want %d/nil", c.name, n, err, c.wantN)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
